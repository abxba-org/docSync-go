package policy_test

// Ported from packages/policy/test/policy-architect.test.ts — Policy Architect
// runner (P10-T14/T15; V-P10-11): counted evidence is mandatory;
// compiler-invalid drafts return with diagnostics and are NEVER promoted; the
// loop is bounded.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// archPolicyExample is the POLICY_EXAMPLE literal (schema.ts) — the canonical
// universal bootstrap policy v17 the TS architect tests use as the valid
// artifact.
func archPolicyExample() map[string]any {
	return map[string]any{
		"schema":         "docsync.jev-policy/v1",
		"policy_version": 17,
		"created_by":     "claude-sonnet (policy-architect)",
		"reason": "Bootstrap universal policy v17: canonical universal questions with conservative calibrated routing " +
			"(high_confidence 0.94, review 0.72) and a calibration no-man\u2019s zone; abstain-safe defaults.",
		"created_at": "2026-01-15T00:00:00.000Z",
		"questions": []any{
			map[string]any{
				"key":    "public_behavior_changed",
				"type":   "boolean",
				"prompt": "Did the change alter runtime behavior visible to users of this repository?",
			},
			map[string]any{
				"key":    "api_contract_changed",
				"type":   "boolean",
				"prompt": "Did the change alter the public contract (exported API surface)?",
			},
			map[string]any{
				"key":     "affected_doc_count",
				"type":    "score",
				"prompt":  "How many known documentation candidates does this change affect?",
				"scoring": map[string]any{"min": 0, "max": 50},
			},
			map[string]any{
				"key":     "documentation_impact",
				"type":    "choice",
				"prompt":  "What is the documentation impact of this change?",
				"choices": []any{"none", "possible", "definite", "unknown"},
			},
		},
		"routing": map[string]any{
			"high_confidence":                 0.94,
			"review":                          0.72,
			"calibration_floor":               0.94,
			"documentation_impact_auto_floor": 0.94,
		},
		"universal":        true,
		"taxonomy_version": 1,
		"evaluation": map[string]any{
			"split":   "evaluation/universal-bootstrap-v17",
			"metrics": map[string]any{"precision": 0.96, "recall": 0.93, "unsafe_auto_updates": 0},
		},
		"unsafe_auto_updates": map[string]any{"enabled": false},
	}
}

// archInputs is the ten-input assembly (the TS INPUTS fixture).
func archInputs() policy.ArchitectLoopInputs {
	return policy.ArchitectLoopInputs{
		PolicyArchitectInputs: policy.PolicyArchitectInputs{
			RepositoryProfile:   map[string]any{"language": "typescript"},
			CurrentPolicy:       archPolicyExample(),
			Manifest:            map[string]any{"docs": []any{"docs/**"}},
			Concepts:            map[string]any{"refunds": []any{"docs/payments.md"}},
			HistoricalPrs:       []map[string]any{{"pr": 481}},
			PriorDecisions:      []map[string]any{{"decision_id": "d1"}},
			FalsePositives:      []map[string]any{{"decision_id": "d2", "reason": "doc already correct"}},
			FalseNegatives:      []map[string]any{{"decision_id": "d3"}},
			ReviewerCorrections: []map[string]any{{"statement": "generated/** is not documentation-relevant"}},
			KnownExceptions:     []map[string]any{{"pattern": "changelog-bot"}},
		},
	}
}

// archValidDraft is the TS VALID_DRAFT (a routing_rule with 17/23 counted
// evidence and a compilable artifact).
func archValidDraft() policy.PolicyArchitectDraft {
	return policy.PolicyArchitectDraft{
		Type: "routing_rule",
		CountedEvidence: []policy.ArchitectCountedEvidence{
			{
				Statement:    "17/23 reviewer overrides targeted generated-API routing",
				SupportCount: 17,
				TotalCount:   23,
			},
		},
		Artifact: archPolicyExample(),
	}
}

// archInvalidArtifact is the TS INVALID_ARTIFACT (the compiler rejects it).
func archInvalidArtifact() map[string]any {
	return map[string]any{"schema": "docsync.not-a-real-policy/v1", "garbage": true}
}

// archFixedIDs is the TS fixedIds(): deterministic, monotonically numbered,
// canonical ULID-shaped ids.
func archFixedIDs() policy.ArchitectNewId {
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

// archClock is the TS CLOCK: a pinned Z timestamp (never the wall clock).
func archClock() string { return "2026-10-08T12:00:00.000Z" }

// archKeyedPort is a replay double keyed by REQUEST IDENTITY (the attempt
// index): every call is dispatched to its own recorded draft and any call
// beyond the recorded identity fails loud (AGENT-behavior replay rule — a
// positional FIFO double cross-wires concurrent callers).
type archKeyedPort struct {
	drafts []policy.PolicyArchitectDraft
	calls  int
	// priorPerCall[i] is the priorFailedAttempt the runner supplied on call
	// i+1 (nil when the runner did not retry).
	priorPerCall []*policy.PriorFailedAttempt
}

func (p *archKeyedPort) Propose(
	input policy.ArchitectLoopInputs,
) (policy.PolicyArchitectDraft, error) {
	p.calls++
	if p.calls > len(p.drafts) {
		return policy.PolicyArchitectDraft{}, fmt.Errorf(
			"architect test port: unexpected request identity (call %d beyond %d recorded drafts)",
			p.calls, len(p.drafts))
	}
	p.priorPerCall = append(p.priorPerCall, input.PriorFailedAttempt)
	return p.drafts[p.calls-1], nil
}

func TestV_P10_11_PolicyArchitectRunner(t *testing.T) {
	t.Run("V-P10-11: the architect on a synthetic FP/FN corpus emits a valid proposal carrying counted evidence (e.g., 17/23); an invalid proposal returns with compiler diagnostics and is never promoted", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock: archClock,
			NewID: archFixedIDs(),
		})
		require.NoError(t, err)
		port := &archKeyedPort{drafts: []policy.PolicyArchitectDraft{archValidDraft()}}
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               port,
			Inputs:             archInputs(),
			PolicyVersionBasis: 1,
		})
		require.NoError(t, err)
		require.True(t, outcome.OK)
		require.Equal(t, schemas.PolicyProposalStatusValidated, outcome.Proposal.Status)
		require.Len(t, outcome.Proposal.CountedEvidence, 1)
		require.Equal(t, 17, outcome.Proposal.CountedEvidence[0].SupportCount)
		require.Equal(t, 23, outcome.Proposal.CountedEvidence[0].TotalCount)
		require.Equal(t, 1, outcome.Proposal.PolicyVersionBasis)
		require.Equal(t, "2026-10-08T12:00:00.000Z", outcome.Proposal.CreatedAt)
		require.Equal(t, schemas.PolicyProposalSchemaID, outcome.Proposal.Schema)
		require.Empty(t, outcome.Diagnostics)

		// the queued lane is explicit — the validated proposal is NOT auto-promoted
		queued := runner.QueueProposal(outcome.Proposal)
		require.Equal(t, schemas.PolicyProposalStatusQueued, queued.Status)
		// QueueProposal never mutates its input (models propose, humans promote).
		require.Equal(t, schemas.PolicyProposalStatusValidated, outcome.Proposal.Status)
	})

	t.Run("a compiler-invalid first draft returns to the architect with diagnostics and the retry validates", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       archFixedIDs(),
			MaxAttempts: 2,
		})
		require.NoError(t, err)
		invalidFirst := archValidDraft()
		invalidFirst.Artifact = archInvalidArtifact()
		port := &archKeyedPort{drafts: []policy.PolicyArchitectDraft{invalidFirst, archValidDraft()}}
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               port,
			Inputs:             archInputs(),
			PolicyVersionBasis: 1,
		})
		require.NoError(t, err)
		require.Equal(t, 2, port.calls)
		require.True(t, outcome.OK)
		require.Equal(t, schemas.PolicyProposalStatusValidated, outcome.Proposal.Status)
		// the prior failed attempt + diagnostics rode along on the retry
		require.Len(t, port.priorPerCall, 2)
		require.Nil(t, port.priorPerCall[0], "the first call must not carry a prior attempt")
		require.NotNil(t, port.priorPerCall[1], "the retry must carry the prior failed attempt (§8.2)")
		require.NotEmpty(t, port.priorPerCall[1].Diagnostics)
		require.Equal(t, "routing_rule", port.priorPerCall[1].Type)
	})

	t.Run("exhausted attempts yield an INVALID proposal with diagnostics — never a partial promotion", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       archFixedIDs(),
			MaxAttempts: 2,
		})
		require.NoError(t, err)
		alwaysInvalid := archValidDraft()
		alwaysInvalid.Artifact = archInvalidArtifact()
		port := &archKeyedPort{drafts: []policy.PolicyArchitectDraft{alwaysInvalid, alwaysInvalid}}
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               port,
			Inputs:             archInputs(),
			PolicyVersionBasis: 1,
		})
		require.NoError(t, err)
		require.False(t, outcome.OK)
		require.Equal(t, schemas.PolicyProposalStatusInvalid, outcome.Proposal.Status)
		require.NotEmpty(t, outcome.Proposal.Diagnostics)
		require.NotEmpty(t, outcome.Diagnostics)
		require.Equal(t, policy.ArchitectRejectReasonCompiler, outcome.Reason)
		require.Equal(t, 2, port.calls)
	})

	t.Run("a counted-evidence-less draft fails closed at the proposal schema", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       archFixedIDs(),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		evidenceLess := archValidDraft()
		evidenceLess.CountedEvidence = []policy.ArchitectCountedEvidence{}
		port := &archKeyedPort{drafts: []policy.PolicyArchitectDraft{evidenceLess}}
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:               port,
			Inputs:             archInputs(),
			PolicyVersionBasis: 1,
		})
		require.NoError(t, err)
		require.False(t, outcome.OK)
		require.Equal(t, policy.ArchitectRejectReasonSchema, outcome.Reason)
		require.Equal(t, schemas.PolicyProposalStatusInvalid, outcome.Proposal.Status)
		// the fallback failure marker is itself schema-valid: 1/1 counted evidence
		require.Len(t, outcome.Proposal.CountedEvidence, 1)
		require.Equal(t, 1, outcome.Proposal.CountedEvidence[0].SupportCount)
		require.Equal(t, 1, outcome.Proposal.CountedEvidence[0].TotalCount)
		require.NotEmpty(t, outcome.Diagnostics)
		for _, diagnostic := range outcome.Diagnostics {
			require.Contains(t, diagnostic, "proposal schema: ")
		}
	})

	t.Run("assembleInputs requires currentPolicy and manifest (ARCH §8.1)", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock: archClock,
			NewID: archFixedIDs(),
		})
		require.NoError(t, err)

		missingPolicy := archInputs().PolicyArchitectInputs
		missingPolicy.CurrentPolicy = map[string]any{}
		_, err = runner.AssembleInputs(missingPolicy)
		require.Error(t, err)
		require.Contains(t, err.Error(), "currentPolicy is required (ARCH §8.1)")

		missingManifest := archInputs().PolicyArchitectInputs
		missingManifest.Manifest = map[string]any{}
		_, err = runner.AssembleInputs(missingManifest)
		require.Error(t, err)
		require.Contains(t, err.Error(), "manifest is required (ARCH §8.1)")

		assembled, err := runner.AssembleInputs(archInputs().PolicyArchitectInputs)
		require.NoError(t, err)
		require.Len(t, assembled.Manifest, 1)
	})

	t.Run("runner construction fails closed without an injected clock or with a non-positive maxAttempts", func(t *testing.T) {
		_, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			NewID: archFixedIDs(),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "architect runner requires an injected clock (no wall-clock defaults)")

		// The TS `!Number.isInteger(maxAttempts) || maxAttempts < 1` check: a
		// Go int cannot be fractional, so the non-integer arm is unrepresentable
		// (deviation noted) — the negative arm is what this asserts.
		_, err = policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       archFixedIDs(),
			MaxAttempts: -1,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "architect maxAttempts must be a positive integer")

		// maxAttempts 0 means "unset" in Go and falls back to the default (the
		// TS `??`).
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock:       archClock,
			NewID:       archFixedIDs(),
			MaxAttempts: 1,
		})
		require.NoError(t, err)
		require.NotNil(t, runner)
	})

	t.Run("a port failure propagates instead of being coerced into a proposal", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock: archClock,
			NewID: archFixedIDs(),
		})
		require.NoError(t, err)
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:   archFailingPort{},
			Inputs: archInputs(),
		})
		require.Error(t, err)
		require.False(t, outcome.OK)
	})

	t.Run("the default id factory mints canonical ULIDs", func(t *testing.T) {
		factory := policy.CreateArchitectUlidFactory()
		first, err := factory()
		require.NoError(t, err)
		second, err := factory()
		require.NoError(t, err)
		require.True(t, len(first) == 26, "expected a 26-char ULID, got %q", first)
		require.True(t, len(second) == 26, "expected a 26-char ULID, got %q", second)
		require.True(t, first <= second, "the ULID family must be monotonic (%q <= %q)", first, second)
	})
}

// archFailingPort is a port that always refuses (model/transport failure).
type archFailingPort struct{}

func (archFailingPort) Propose(policy.ArchitectLoopInputs) (policy.PolicyArchitectDraft, error) {
	return policy.PolicyArchitectDraft{}, errors.New("openrouter: policy-architect role unavailable")
}

func TestArchitectFailClosedSurfaces(t *testing.T) {
	t.Run("a non-JSON artifact fails closed at the wire seam instead of panicking", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock: archClock,
			NewID: archFixedIDs(),
		})
		require.NoError(t, err)
		draft := archValidDraft()
		draft.Artifact = map[string]any{"schema": "docsync.jev-policy/v1", "bad": make(chan int)}
		outcome, err := runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Port:   &archKeyedPort{drafts: []policy.PolicyArchitectDraft{draft}},
			Inputs: archInputs(),
		})
		require.Error(t, err)
		require.False(t, outcome.OK)
		require.Contains(t, err.Error(), "could not serialize the proposal wire form")
		require.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
	})

	t.Run("a nil port is refused (the port is the only model seam)", func(t *testing.T) {
		runner, err := policy.CreatePolicyArchitectRunner(policy.PolicyArchitectRunnerOptions{
			Clock: archClock,
			NewID: archFixedIDs(),
		})
		require.NoError(t, err)
		_, err = runner.ProposeAndValidate(policy.PolicyArchitectProposeInput{
			Inputs: archInputs(),
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "architect runner requires a port")
	})
}
