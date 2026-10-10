package policy_test

// Ported from packages/policy/test/claim-extractor.test.ts (P11-T12/T13;
// V-P11-5) and packages/policy/test/extractor-trial.test.ts (P11-T14;
// V-P11-6): extracted claims are stored as proposals; the truth gate is
// deterministic zero-LLM (verbatim quote + anchor resolution); an unverified
// proposal never drives an auto action; ADDITIONS ONLY — the runner never
// touches a claim store. The A4 trial records per-doc verification stats plus
// the fixture-data verdict and declares the Tier-2 prose-heavy fallback.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// extDocContent is the TS DOC_CONTENT fixture (docs/payments.md).
func extDocContent() string {
	return strings.Join([]string{
		"# Payments",
		"",
		"## Refunds",
		"",
		"Refunds require an idempotency key.",
		"",
		"## Webhooks",
		"",
		"Webhook retries use exponential backoff.",
		"",
	}, "\n")
}

// extDocStructure is the TS DOC_STRUCTURE fixture.
func extDocStructure() policy.ClaimExtractorDocStructure {
	return policy.ClaimExtractorDocStructure{
		SectionHeadings: []string{"## Refunds", "## Webhooks"},
		LineCount:       len(strings.Split(extDocContent(), "\n")),
	}
}

// extInputs is the TS INPUTS fixture.
func extInputs() policy.ClaimExtractorInputs {
	return policy.ClaimExtractorInputs{
		DocPath:        "docs/payments.md",
		DocContent:     extDocContent(),
		DocStructure:   extDocStructure(),
		ExistingClaims: []policy.ExistingClaimReference{},
		PolicyVersion:  1,
	}
}

// extFixedIDs is the TS fixedIds(): deterministic, monotonically numbered,
// canonical ULID-shaped ids.
func extFixedIDs() policy.ExtractorNewId {
	n := 0
	return func() (string, error) {
		n++
		id := fmt.Sprintf("01HTXSCHEMA1EXAMP1ES%06d0", n)
		if len(id) > 26 {
			id = id[:26]
		}
		return id, nil
	}
}

// extClock is the TS CLOCK: a pinned Z timestamp (never the wall clock).
func extClock() string { return "2026-10-08T12:00:00.000Z" }

// extGoodDraft is the TS GOOD_DRAFT (a verbatim quote + a resolving anchor).
func extGoodDraft() policy.ClaimExtractorDraft {
	return policy.ClaimExtractorDraft{
		Claims: []policy.ExtractedClaimDraft{
			{
				ClaimText: "Refunds require an idempotency key.",
				CandidateAnchor: schemas.ClaimCandidateAnchor{
					SectionHeading: extStrPtr("Refunds"),
					LineHint:       extIntPtr(5),
				},
				SourceEvidence: policy.ClaimSourceEvidenceDraft{
					Quote:   "Refunds require an idempotency key.",
					DocPath: "docs/payments.md",
				},
				ExtractionModel: "z-ai/glm-4.7",
			},
		},
	}
}

// extKeyedPort is a replay double keyed by REQUEST IDENTITY (the attempt
// index): each call is dispatched to its own recorded draft and the prior
// failed attempt is captured for assertions. Beyond the recorded identities it
// REPLAYS the last recorded draft — exactly the TS `portReturning` helper
// (`drafts[Math.min(calls, drafts.length) - 1]`), so a bounded loop over a
// shorter draft list behaves like the TS retry fixture. An empty draft list
// still fails loud (that is a broken fixture, not a replay).
type extKeyedPort struct {
	drafts       []policy.ClaimExtractorDraft
	calls        int
	priorPerCall []*policy.PriorExtractionAttempt
}

func (p *extKeyedPort) Extract(
	input policy.ClaimExtractorLoopInputs,
) (policy.ClaimExtractorDraft, error) {
	p.calls++
	if len(p.drafts) == 0 {
		return policy.ClaimExtractorDraft{}, fmt.Errorf(
			"extractor test port: no recorded draft for request identity (call %d)", p.calls)
	}
	index := p.calls - 1
	if index > len(p.drafts)-1 {
		index = len(p.drafts) - 1
	}
	p.priorPerCall = append(p.priorPerCall, input.PriorFailedAttempt)
	return p.drafts[index], nil
}

// extDocKeyedPort is a replay double keyed by REQUEST IDENTITY (the doc path):
// the trial supplies a per-doc draft map, and an unknown doc path fails loud.
type extDocKeyedPort struct {
	byDoc map[string]policy.ClaimExtractorDraft
	seen  []string
}

func (p *extDocKeyedPort) Extract(
	input policy.ClaimExtractorLoopInputs,
) (policy.ClaimExtractorDraft, error) {
	p.seen = append(p.seen, input.DocPath)
	draft, ok := p.byDoc[input.DocPath]
	if !ok {
		return policy.ClaimExtractorDraft{}, fmt.Errorf(
			"extractor trial port: no fixture draft for %s (fail loud on unexpected request identity)",
			input.DocPath)
	}
	return draft, nil
}

// extNewRunner builds a runner with the pinned clock/id factory.
func extNewRunner(t *testing.T, maxAttempts int) policy.ClaimExtractorRunner {
	t.Helper()
	runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
		Clock: extClock,
		NewID: extFixedIDs(),
	})
	require.NoError(t, err)
	if maxAttempts > 0 {
		runner, err = policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock:       extClock,
			NewID:       extFixedIDs(),
			MaxAttempts: maxAttempts,
		})
		require.NoError(t, err)
	}
	return runner
}

func TestV_P11_5_ClaimExtractorRunner(t *testing.T) {
	t.Run("V-P11-5: proposeAndValidate yields a status-validated docsync.claim-proposal/v1 artifact; verifyProposals confirms a verbatim quote + resolving anchor into verified_claims", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)
		proposal := outcome.Proposal
		require.Equal(t, "validated", proposal.Status)
		require.Equal(t, "docsync.claim-proposal/v1", proposal.Schema)
		require.Len(t, proposal.Claims, 1)
		require.Equal(t, "z-ai/glm-4.7", proposal.Claims[0].ExtractionModel)
		require.Equal(t, "docs/payments.md", proposal.DocPath)
		require.Equal(t, "2026-10-08T12:00:00.000Z", proposal.CreatedAt)
		require.Empty(t, proposal.VerifiedClaims, "the runner never sets verified_claims — the truth gate does")

		gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     proposal,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate.Unverified)
		require.Len(t, gate.VerifiedClaims, 1)
		require.Equal(t, "source_confirmed", gate.VerifiedClaims[0].Verification)
		require.Equal(t, proposal.Claims[0].ClaimID, gate.VerifiedClaims[0].ClaimID)
		require.Equal(t, "2026-10-08T12:00:00.000Z", gate.Proposal.VerifiedClaims[0].VerifiedAt)
		require.Equal(t, "validated", gate.Proposal.Status)
	})

	t.Run("a tampered quote is unverifiable and EXPLICITLY flagged — the proposal stays validated and never drives an auto action", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)

		tampered := outcome.Proposal
		for i := range tampered.Claims {
			tampered.Claims[i].SourceEvidence.Quote = "Refunds require a UNIQUE idempotency key."
		}
		gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     tampered,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate.VerifiedClaims)
		require.Len(t, gate.Unverified, 1)
		require.Equal(t, policy.ClaimRejectReasonQuoteNotFound, gate.Unverified[0].Reason)
		require.NotEmpty(t, gate.Unverified[0].Diagnostics)
		require.Contains(t, gate.Unverified[0].Diagnostics[0], "source_evidence.quote not found verbatim")
		// The proposal is NOT flipped to a trusted state while a claim is unverified.
		require.Equal(t, "validated", gate.Proposal.Status)
		require.Nil(t, gate.Proposal.VerifiedClaims)
	})

	t.Run("an anchor that resolves by NONE of the three forms is flagged; each form resolves on its own", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)

		unanchorable := outcome.Proposal
		for i := range unanchorable.Claims {
			unanchorable.Claims[i].CandidateAnchor = schemas.ClaimCandidateAnchor{
				SectionHeading: extStrPtr("Nonexistent Section"),
			}
		}
		gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     unanchorable,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate.VerifiedClaims)
		require.Len(t, gate.Unverified, 1)
		require.Equal(t, policy.ClaimRejectReasonAnchorUnresolved, gate.Unverified[0].Reason)
		require.Contains(t, gate.Unverified[0].Diagnostics[0], "candidate_anchor unresolved")

		// content_hash anchor: sha256 over the whitespace-collapsed doc line.
		hash := docutil.ContentHash("Refunds require an idempotency key.")
		hashAnchored := outcome.Proposal
		for i := range hashAnchored.Claims {
			hashAnchored.Claims[i].CandidateAnchor = schemas.ClaimCandidateAnchor{
				ContentHash: &hash,
			}
		}
		gate2, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     hashAnchored,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate2.Unverified)
		require.Len(t, gate2.VerifiedClaims, 1)

		// line_hint alone resolves; out-of-range line_hint does not.
		lineAnchored := outcome.Proposal
		for i := range lineAnchored.Claims {
			lineAnchored.Claims[i].CandidateAnchor = schemas.ClaimCandidateAnchor{
				LineHint: extIntPtr(5),
			}
		}
		gate3, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     lineAnchored,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Len(t, gate3.VerifiedClaims, 1)

		outOfRange := outcome.Proposal
		outOfRange.Claims[0].CandidateAnchor = schemas.ClaimCandidateAnchor{
			LineHint: extIntPtr(999),
		}
		gate4, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     outOfRange,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate4.VerifiedClaims)
		require.Len(t, gate4.Unverified, 1)
		require.Equal(t, policy.ClaimRejectReasonAnchorUnresolved, gate4.Unverified[0].Reason)
	})

	t.Run("an empty claim set is a valid proposal (an honest extractor may extract nothing)", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{{Claims: []policy.ExtractedClaimDraft{}}}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)
		require.Empty(t, outcome.Proposal.Claims)
		gate, err := runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     outcome.Proposal,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.NoError(t, err)
		require.Empty(t, gate.VerifiedClaims)
		require.Empty(t, gate.Unverified)
	})

	t.Run("a schema-invalid first draft returns to the extractor with diagnostics and the retry validates; exhausted attempts yield status invalid", func(t *testing.T) {
		runner := extNewRunner(t, 2)
		badDraft := policy.ClaimExtractorDraft{
			Claims: []policy.ExtractedClaimDraft{
				{
					ClaimText:       "x",
					CandidateAnchor: schemas.ClaimCandidateAnchor{},
					SourceEvidence: policy.ClaimSourceEvidenceDraft{
						Quote:   "",
						DocPath: "docs/payments.md",
					},
					ExtractionModel: "z-ai/glm-4.7",
				},
			},
		}
		port := &extKeyedPort{drafts: []policy.ClaimExtractorDraft{badDraft, extGoodDraft()}}
		retry, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   port,
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, retry.OK)
		require.Equal(t, "validated", retry.Proposal.Status)
		require.Len(t, port.priorPerCall, 2)
		require.Nil(t, port.priorPerCall[0])
		require.NotNil(t, port.priorPerCall[1], "the retry must carry the prior failed attempt")
		require.NotEmpty(t, port.priorPerCall[1].Diagnostics)
		require.Contains(t, strings.Join(port.priorPerCall[1].Diagnostics, "\n"), "claim proposal schema: ")

		exhausted, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{badDraft}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.False(t, exhausted.OK)
		require.Equal(t, "invalid", exhausted.Proposal.Status)
		require.NotEmpty(t, exhausted.Proposal.Diagnostics)
		require.NotEmpty(t, exhausted.Diagnostics)
		require.Equal(t, policy.ClaimExtractorRejectReasonSchema, exhausted.Reason)
	})

	t.Run("the truth gate refuses non-validated proposals (fail closed)", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		outcome, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)
		draft := outcome.Proposal
		draft.Status = "draft"
		_, err = runner.VerifyProposals(policy.ClaimVerifyInput{
			Proposal:     draft,
			DocContent:   extDocContent(),
			DocStructure: extDocStructure(),
		})
		require.Error(t, err)
		var extractorErr *policy.ClaimExtractorError
		require.True(t, errors.As(err, &extractorErr),
			"expected a *policy.ClaimExtractorError, got %T: %v", err, err)
		require.Contains(t, err.Error(),
			"truth gate consumes validated proposals only — refuse to verify a draft or invalid proposal")
	})

	t.Run("ADDITIONS ONLY: the runner surface has no claim-store access — nothing can overwrite existing claim rows", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		// Shape assertion (the TS Object.keys(runner) check, Go form): the runner
		// surface exposes ONLY extraction + verification — no write/save/store/
		// upsert/replace/overwrite/commit method exists, so existing
		// (hand-anchored / manifest-declared) claim-store rows can never be
		// overwritten by extraction; extraction proposes ADDITIONS only.
		methods := reflect.TypeOf(runner)
		require.Equal(t, 2, methods.NumMethod(),
			"the extractor runner must expose exactly proposeAndValidate + verifyProposals")
		names := make([]string, 0, methods.NumMethod())
		for i := 0; i < methods.NumMethod(); i++ {
			names = append(names, methods.Method(i).Name)
		}
		require.ElementsMatch(t, []string{"ProposeAndValidate", "VerifyProposals"}, names)
		forbidden := regexp.MustCompile(`(?i)store|save|write|upsert|replace|overwrite|commit`)
		for _, name := range names {
			require.False(t, forbidden.MatchString(name),
				"the extractor runner must not expose %s", name)
		}
	})

	t.Run("whitespace-collapse canon and the deterministic collapsed digest", func(t *testing.T) {
		require.Equal(t, "a b c", policy.CollapseWhitespace("  a\t\n b   c  "))
		require.Equal(t, "a b", policy.CollapseWhitespace("\u00a0a\u2003b\ufeff"))
		require.Equal(t, "", policy.CollapseWhitespace("   \n\t "))
		require.Equal(t, "no change", policy.CollapseWhitespace("no change"))
		// The digest is the bare 64-hex sha256 over the collapsed text.
		digest := sha256.Sum256([]byte("Refunds require an idempotency key."))
		require.Equal(t, hex.EncodeToString(digest[:]), policy.CollapsedSha256("Refunds require an idempotency key."))
		require.Equal(t, policy.CollapsedSha256("a  b"), policy.CollapsedSha256("a b"),
			"the digest must be whitespace-collapse invariant")
	})

	t.Run("runner construction fails closed without an injected clock or with a non-positive maxAttempts", func(t *testing.T) {
		_, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			NewID: extFixedIDs(),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(),
			"claim extractor runner requires an injected clock (no wall-clock defaults)")

		_, err = policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock:       extClock,
			NewID:       extFixedIDs(),
			MaxAttempts: -3,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "claim extractor maxAttempts must be a positive integer")

		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: extFixedIDs(),
		})
		require.NoError(t, err)
		require.Equal(t, 2, policy.ExtractorMaxAttemptsDefault)
		require.NotNil(t, runner)
		require.NotNil(t, policy.CreateExtractorUlidFactory())
	})
}

func extStrPtr(value string) *string { return &value }

func extIntPtr(value int) *int { return &value }

/* ---------------------------------------------- the A4 extractor-quality trial */

// extTrialDocA/B/C are the TS DOC_A / DOC_B / DOC_C fixtures.
func extTrialDocA() (string, []string, int) {
	lines := []string{
		"# Payments",
		"",
		"## Refunds",
		"",
		"Refunds require an idempotency key.",
		"",
		"## Webhooks",
		"",
		"Webhook retries use exponential backoff.",
		"",
	}
	return strings.Join(lines, "\n"), []string{"## Refunds", "## Webhooks"}, len(lines)
}

func extTrialDocB() (string, []string, int) {
	lines := []string{
		"# Ingest",
		"",
		"## Limits",
		"",
		"Ingest caps requests at 100 per minute.",
		"",
		"## Retention",
		"",
		"Retention defaults to 30 days.",
		"",
	}
	return strings.Join(lines, "\n"), []string{"## Limits", "## Retention"}, len(lines)
}

func extTrialDocC() (string, []string, int) {
	lines := []string{"# Notes", ""}
	for len(lines) < 96 {
		lines = append(lines,
			"The service grew out of an earlier prototype and its behaviour settled gradually over time.")
	}
	return strings.Join(lines, "\n"), []string{"# Notes"}, len(lines)
}

// extTrialClaimFor is the TS claimFor helper.
func extTrialClaimFor(docPath, quote string, lineHint int) policy.ExtractedClaimDraft {
	return policy.ExtractedClaimDraft{
		ClaimText:       quote,
		CandidateAnchor: schemas.ClaimCandidateAnchor{LineHint: extIntPtr(lineHint)},
		SourceEvidence:  policy.ClaimSourceEvidenceDraft{Quote: quote, DocPath: docPath},
		ExtractionModel: "z-ai/glm-4.7",
	}
}

// extTrialPort builds the trial port keyed by doc path (request identity).
func extTrialPort(byDoc map[string]policy.ClaimExtractorDraft) *extDocKeyedPort {
	return &extDocKeyedPort{byDoc: byDoc}
}

func extTrialDocs() []policy.ExtractorTrialDoc {
	docA, headingsA, linesA := extTrialDocA()
	docB, headingsB, linesB := extTrialDocB()
	docC, headingsC, linesC := extTrialDocC()
	return []policy.ExtractorTrialDoc{
		{DocPath: "docs/payments.md", Content: docA, SectionHeadings: headingsA, LineCount: linesA},
		{DocPath: "docs/ingest.md", Content: docB, SectionHeadings: headingsB, LineCount: linesB},
		{DocPath: "docs/notes.md", Content: docC, SectionHeadings: headingsC, LineCount: linesC},
	}
}

func TestV_P11_6_ExtractorQualityTrial(t *testing.T) {
	t.Run("V-P11-6: prose-heavy fixture falls back to section-level checking with the fallback declared in the run record", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: extFixedIDs(),
		})
		require.NoError(t, err)
		verdict, err := policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port: extTrialPort(map[string]policy.ClaimExtractorDraft{
				"docs/payments.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/payments.md", "Refunds require an idempotency key.", 5),
					extTrialClaimFor("docs/payments.md", "Webhook retries use exponential backoff.", 9),
				}},
				"docs/ingest.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at 100 per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 30 days.", 9),
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at 100 per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 90 days.", 9),
				}},
				"docs/notes.md": {Claims: []policy.ExtractedClaimDraft{}},
			}),
			Docs:  extTrialDocs(),
			Clock: extClock,
		})
		require.NoError(t, err)

		require.Equal(t, "fixture", verdict.DataSource)
		require.Equal(t, policy.ClaimVerificationRateFloor, verdict.Threshold)
		require.InDelta(t, 0.8, policy.ClaimVerificationRateFloor, 1e-12)
		require.Equal(t, "2026-10-08T12:00:00.000Z", verdict.TrialAt)

		require.Len(t, verdict.PerDoc, 3)

		docA := verdict.PerDoc[0]
		require.Equal(t, "docs/payments.md", docA.DocPath)
		require.Equal(t, 2, docA.ClaimsExtracted)
		require.Equal(t, 2, docA.ClaimsVerified)
		require.Equal(t, 0, docA.ClaimsUnverified)
		require.InDelta(t, 1.0, docA.VerificationRate, 1e-12)
		require.Equal(t, policy.ExtractorQuoteAllVerbatim, docA.QuoteIntegrity)
		require.False(t, docA.ProseHeavy)

		docB := verdict.PerDoc[1]
		require.Equal(t, 4, docB.ClaimsExtracted)
		require.Equal(t, 3, docB.ClaimsVerified)
		require.Equal(t, 1, docB.ClaimsUnverified)
		require.InDelta(t, 0.75, docB.VerificationRate, 1e-12)
		// The tampered quotes are EXPLICITLY recorded as tampered integrity.
		require.Equal(t, policy.ExtractorQuoteTampered, docB.QuoteIntegrity)

		docC := verdict.PerDoc[2]
		require.Equal(t, 0, docC.ClaimsExtracted)
		require.True(t, docC.ProseHeavy)
		require.Contains(t, docC.ProseHeavyReason, "prose-heavy")

		// Aggregate: 5 verified / 6 extracted ≈ 0.833 ≥ the 0.8 floor → pass.
		require.Equal(t, 6, verdict.ClaimsExtracted)
		require.Equal(t, 5, verdict.ClaimsVerified)
		require.InDelta(t, 5.0/6.0, verdict.VerificationRate, 1e-12)
		require.Equal(t, policy.ExtractorTrialVerdictPass, verdict.Verdict)

		// The declared Tier-2 fallback: recorded on the run-record seam, never silent.
		require.Equal(t, []string{"docs/notes.md"}, verdict.ProseHeavyDocs)
		require.Equal(t, []policy.ExtractorTrialFallbackDeclaration{
			{DocPath: "docs/notes.md", Note: policy.ProseHeavyFallbackNote},
		}, verdict.FallbackDeclarations)
	})

	t.Run("a tampered-quote fixture drops the aggregate rate below the floor (fail); verifying the dropped claim crosses it (pass)", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: extFixedIDs(),
		})
		require.NoError(t, err)
		docA, headingsA, linesA := extTrialDocA()
		docB, headingsB, linesB := extTrialDocB()
		docs := []policy.ExtractorTrialDoc{
			{DocPath: "docs/payments.md", Content: docA, SectionHeadings: headingsA, LineCount: linesA},
			{DocPath: "docs/ingest.md", Content: docB, SectionHeadings: headingsB, LineCount: linesB},
		}

		belowFloor, err := policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port: extTrialPort(map[string]policy.ClaimExtractorDraft{
				"docs/payments.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/payments.md", "Refunds require an idempotency key.", 5),
					extTrialClaimFor("docs/payments.md", "Webhook retries use exponential backoff.", 9),
					extTrialClaimFor("docs/payments.md", "Refunds require an idempotency key.", 5),
				}},
				"docs/ingest.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at 100 per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at ONE HUNDRED per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 30 days.", 9),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 90 days.", 9),
				}},
			}),
			Docs:  docs,
			Clock: extClock,
		})
		require.NoError(t, err)
		// 5 verified / 7 extracted ≈ 0.714 — below the 0.8 floor: fail.
		require.Equal(t, 7, belowFloor.ClaimsExtracted)
		require.Equal(t, 5, belowFloor.ClaimsVerified)
		require.InDelta(t, 5.0/7.0, belowFloor.VerificationRate, 1e-12)
		require.Equal(t, policy.ExtractorTrialVerdictFail, belowFloor.Verdict)
		require.Equal(t, policy.ExtractorQuoteTampered, belowFloor.PerDoc[1].QuoteIntegrity)

		// Fixing ONE tampered quote crosses the floor: 6/7 ≈ 0.857 ≥ 0.8 → pass.
		crossing, err := policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port: extTrialPort(map[string]policy.ClaimExtractorDraft{
				"docs/payments.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/payments.md", "Refunds require an idempotency key.", 5),
					extTrialClaimFor("docs/payments.md", "Webhook retries use exponential backoff.", 9),
					extTrialClaimFor("docs/payments.md", "Refunds require an idempotency key.", 5),
				}},
				"docs/ingest.md": {Claims: []policy.ExtractedClaimDraft{
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at 100 per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Ingest caps requests at 100 per minute.", 5),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 30 days.", 9),
					extTrialClaimFor("docs/ingest.md", "Retention defaults to 90 days.", 9),
				}},
			}),
			Docs:  docs,
			Clock: extClock,
		})
		require.NoError(t, err)
		require.InDelta(t, 6.0/7.0, crossing.VerificationRate, 1e-12)
		require.Equal(t, policy.ExtractorTrialVerdictPass, crossing.Verdict)
		require.Equal(t, "fixture", crossing.DataSource)
	})

	t.Run("a trial that extracts nothing anywhere demonstrates nothing and FAILS CLOSED", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: extFixedIDs(),
		})
		require.NoError(t, err)
		docC, headingsC, linesC := extTrialDocC()
		verdict, err := policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port: extTrialPort(map[string]policy.ClaimExtractorDraft{
				"docs/notes.md": {Claims: []policy.ExtractedClaimDraft{}},
			}),
			Docs:  []policy.ExtractorTrialDoc{{DocPath: "docs/notes.md", Content: docC, SectionHeadings: headingsC, LineCount: linesC}},
			Clock: extClock,
		})
		require.NoError(t, err)
		require.Equal(t, 0, verdict.ClaimsExtracted)
		require.InDelta(t, 0.0, verdict.VerificationRate, 1e-12)
		require.Equal(t, policy.ExtractorTrialVerdictFail, verdict.Verdict)
		require.Len(t, verdict.FallbackDeclarations, 1)
		require.Equal(t, "docs/notes.md", verdict.ProseHeavyDocs[0])
	})

	t.Run("detectProseHeavyDoc: sparse headings with no extractable claims are prose-heavy; dense headings or a real claim yield are not; short docs never fire", func(t *testing.T) {
		docC, headingsC, _ := extTrialDocC()
		docA, headingsA, _ := extTrialDocA()

		sparse := policy.DetectProseHeavyDoc(docC, headingsC, extIntPtr(0))
		require.True(t, sparse.ProseHeavy)
		require.Contains(t, sparse.Reason, "section-level checking")

		dense := policy.DetectProseHeavyDoc(docA, headingsA, extIntPtr(2))
		require.False(t, dense.ProseHeavy)

		// Sparse headings BUT a real claim yield: not prose-heavy (claims anchor it).
		anchored := policy.DetectProseHeavyDoc(docC, headingsC, extIntPtr(3))
		require.False(t, anchored.ProseHeavy)
		require.Contains(t, anchored.Reason, "3 extractable claim(s)")

		// Structural-only call (claims yield not supplied): sparse ⇒ prose-heavy.
		require.True(t, policy.DetectProseHeavyDoc(docC, headingsC, nil).ProseHeavy)

		// Short docs never fire regardless of headings.
		short := policy.DetectProseHeavyDoc("one line\n", nil, extIntPtr(0))
		require.False(t, short.ProseHeavy)
		require.Contains(t, short.Reason, "meets the density floor")
	})

	t.Run("the floor is versioned trial data in (0, 1] — never a hard-coded engine threshold (D11-adjacent)", func(t *testing.T) {
		require.InDelta(t, 0.8, policy.ClaimVerificationRateFloor, 1e-12)
		require.Greater(t, policy.ClaimVerificationRateFloor, 0.0)
		require.LessOrEqual(t, policy.ClaimVerificationRateFloor, 1.0)
		require.Contains(t, policy.ProseHeavyFallbackNote, "section-level checking")
		require.Equal(t, 80, policy.ProseHeavyHeadingDensityDivisor)
		require.Equal(t, 40, policy.ProseHeavyMinLines)
	})

	t.Run("the trial fails loud on an unexpected request identity and requires a clock", func(t *testing.T) {
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: extFixedIDs(),
		})
		require.NoError(t, err)
		_, err = policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port:   extTrialPort(map[string]policy.ClaimExtractorDraft{}),
			Docs:   extTrialDocs(),
			Clock:  extClock,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "no fixture draft for docs/payments.md")

		_, err = policy.RunExtractorQualityTrial(policy.ExtractorQualityTrialInput{
			Runner: runner,
			Port:   extTrialPort(map[string]policy.ClaimExtractorDraft{}),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(),
			"extractor quality trial requires an injected clock (no wall-clock defaults)")
	})
}

func TestExtractorFailClosedSurfaces(t *testing.T) {
	t.Run("an id-minting failure fails closed instead of dropping a claim", func(t *testing.T) {
		generated := 0
		runner, err := policy.CreateClaimExtractorRunner(policy.ClaimExtractorRunnerOptions{
			Clock: extClock,
			NewID: func() (string, error) {
				generated++
				if generated > 1 {
					return "", errors.New("ulid: crypto/rand unavailable")
				}
				return "01HTXSCHEMA1EXAMP1ES000001", nil
			},
		})
		require.NoError(t, err)
		_, err = runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Port:   &extKeyedPort{drafts: []policy.ClaimExtractorDraft{extGoodDraft()}},
			Inputs: extInputs(),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "could not mint an id")
	})

	t.Run("a nil port is refused (the port is the only model seam)", func(t *testing.T) {
		runner := extNewRunner(t, 0)
		_, err := runner.ProposeAndValidate(policy.ClaimExtractorProposeInput{
			Inputs: extInputs(),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "claim extractor runner requires a port")
	})

	t.Run("the extractor shares the architect ULID family", func(t *testing.T) {
		factory := policy.CreateExtractorUlidFactory()
		first, err := factory()
		require.NoError(t, err)
		second, err := factory()
		require.NoError(t, err)
		require.Len(t, first, 26)
		require.Len(t, second, 26)
		require.True(t, first <= second)
	})

	t.Run("the claim wire digest matches the Go schema example digest (OD-R42 coherence)", func(t *testing.T) {
		// The bare 64-hex sha256 of a collapsed claim text is what the
		// candidate_anchor content_hash form must carry (doc-patch anchors use
		// the same convention).
		digest := policy.CollapsedSha256("Refunds require an idempotency key.")
		require.Regexp(t, `^[0-9a-f]{64}$`, digest)
		require.Equal(t, digest, policy.CollapsedSha256("  Refunds   require an idempotency\nkey.  "))
	})
}
