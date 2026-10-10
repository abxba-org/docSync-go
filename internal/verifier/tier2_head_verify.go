package verifier

// Tier-2 head verification (port of tier2/head-verify.ts; P4-T10; PLAN §5.4
// item 10; ARCH §4.8 Tier-2): per-claim verification of the BOUND claims
// against the source of truth at the PR head. The `workspaceSources` injected
// at the orchestrator edge ARE that head content (manifest-scoped,
// workspace-restricted — tier2_types.go), so this check never touches fs, git
// or network: it compares claims against injected bytes only.
//
// Scope: ONLY claims with a `bound` binding are this function's concern —
// BindClaims produced one binding per decomposed claim, and the CALLER routes
// unbound claims `unanchored` via the tracker before calling (unanchored ⇒
// suggest, never auto — PLAN §5.6/ARCH §4.8; a guessed anchor is never
// fabricated). Per bound claim, in index order:
//
//   - `value` / `error_code`: the asserted tokens (re-extracted via
//     ExtractClaimedTokens — never re-inferred) must appear as literal
//     substrings in the bound file's content, restricted to the ±10-line window
//     around `binding.line` when the line is known, else the whole file. Any
//     asserted token matching ⇒ `supported`; none ⇒ `mismatched` naming the
//     claim id, the expected token(s) and the bound file:line. The any-token
//     rule mirrors the binding search (tier2_bind.go): a claim binds at the
//     first line ANY token matches, so verification asks the same question of
//     the same evidence.
//   - `signature`: whitespace-normalized comparison of the full signature span
//     against the same window — declarations get reformatted, so the span must
//     survive whitespace removal (bind.ts's looser identifier-prefix tolerance
//     is a BINDING fallback only, never verification evidence).
//   - `behavioral`: OD-R09 — not deterministically verifiable in V1. The
//     `ContractCorroborated` port decides: `true` ⇒ `supported` (corroborated by
//     a declared contract clause), anything else ⇒ `behavioral-unverified`
//     (routed to suggest, never auto). NEVER `mismatched`.
//   - bound file absent from `workspaceSources` ⇒ `unchecked` — a declared gap
//     (A4 posture), recorded with evidence, never guessed.
//   - a token-bearing kind with an EMPTY extracted token surface ⇒ `unchecked`
//     (fail-closed: nothing is asserted, so nothing can mismatch — abstain,
//     don't guess, §3 rule 6).
//   - `other` ⇒ `unchecked` (`recorded, not verified in V1`).
//
// V1 posture note on workspace-token bindings (declared, never silent): a
// `workspace-token`-bound claim is verified TAUTOLOGICALLY against the same
// evidence that bound it — the verification window contains the matched token
// by construction, so 'supported' for such claims attests DISCOVERABILITY of
// the token in the head source, not the truth of the claim. The discriminating
// power of head-verification therefore applies to `claim-store`-bound claims
// (an independently recorded anchor and line window); workspace bindings prove
// discoverability, not truth.
//
// Check record: id `head-verification` (closed check-id vocabulary, schemas
// verification-report.go); status `fail` iff ≥1 claim is `mismatched`,
// `skipped` when the decomposition is empty, else `pass` with per-status
// counts. `inputs` pins the consumed evidence — sorted deduped `<file>@<sha>`
// entries from `input.sourceHeadShas` for the bound files, when SHAs are
// available (invariant 10 — provenance or it didn't happen).
//
// Pure and deterministic (module doc): no fs, no git, no network, no clock of
// its own, no Date/random — the same inputs always yield deep-equal per-claim
// statuses and check record.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// HeadVerifyDeps are injected ports for the head-verification check.
// Behavioral predicates are not deterministically verifiable in V1 (OD-R09);
// corroboration is the one bounded escape hatch — the application decides
// whether a declared contract clause backs the predicate, exactly as it decides
// permission everywhere else. An absent port or any non-`true` answer abstains
// to `behavioral-unverified` (fail closed, §3 rule 9).
type HeadVerifyDeps struct {
	// ContractCorroborated reports whether the document's declared contracts
	// (P1-T12 vector) corroborate the behavioral predicate. `true` ⇒ the claim
	// records `supported` with the contract detail; anything else abstains to
	// `behavioral-unverified`.
	ContractCorroborated func(claim DecomposedClaim) bool
}

// windowRadius is the ±10-line verification window around a known binding line
// (1-based, inclusive).
const windowRadius = 10

// The OD-R09 corroboration detail (contract-backed behavioral predicate).
const corroboratedDetail = "corroborated by declared contract clause"

// The OD-R09 abstention detail — suggest, never auto (invariant posture).
const behavioralUnverifiedDetail = "behavioral predicate not deterministically verifiable in V1 (OD-R09) — routed to suggest, never auto"

// The `other`-kind detail: carried by the report, verified by nothing in V1.
const otherDetail = "recorded, not verified in V1"

/* ------------------------------------------------------------------ helpers */

// tier2ClaimIDFor is the tracker's claim-id convention (status.ts claimIdFor —
// the store id when present, else the stable `decomposed-<index>` fallback),
// kept local so mismatch details name the same id the tracker snapshot emits.
func tier2ClaimIDFor(claim DecomposedClaim) string {
	if claim.ExistingClaimID != "" {
		return claim.ExistingClaimID
	}
	return fmt.Sprintf("decomposed-%d", claim.Index)
}

// tier2EvidenceWindow is the head-content region one claim is verified
// against: the whole file when the binding carries no line, else the ±10-line
// window around it (clamped to the file). A bound line beyond the file yields
// an empty window — the stale anchor verifies nothing (fail closed, never a
// widened guess).
func tier2EvidenceWindow(content string, line *int) string {
	lines := claims.SplitDocLines(content)
	if line == nil {
		return strings.Join(lines, "\n")
	}
	start := *line - windowRadius
	if start < 1 {
		start = 1
	}
	end := *line + windowRadius
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n")
}

/* ---------------------------------------------------------------- exports */

// CheckHeadVerification verifies each BOUND decomposed claim — IN INDEX ORDER
// (a copy is sorted by `index` so unordered input stays deterministic) —
// against the head content in `input.workspaceSources`, recording every
// outcome on the tracker (the caller routes unbound claims `unanchored` before
// calling; this function never touches them). See the module doc for the
// per-kind semantics.
//
// The tracker's fail-closed validation (unknown index / invalid status) is
// surfaced as an error instead of the TS throw: the decomposition, the
// bindings and the tracker all come from one run, so a violation is an
// internal composition bug, never a silent no-op.
//
// Pure and deterministic: the same inputs always yield deep-equal statuses and
// check record; nothing outside `input`, `decomposed`, `bindings` and the
// injected `deps` port is read.
func CheckHeadVerification(
	input Tier2Input,
	decomposed []DecomposedClaim,
	bindings []ClaimBinding,
	tracker *ClaimStatusTracker,
	deps *HeadVerifyDeps,
) (schemas.VerificationCheck, error) {
	if len(decomposed) == 0 {
		return schemas.VerificationCheck{
			ID:     "head-verification",
			Status: schemas.CheckSkipped,
			Detail: tier2StringPtr("patch proposes no claims"),
		}, nil
	}

	workspaceSources := input.WorkspaceSources
	sourceHeadShas := input.SourceHeadShas
	bindingByIndex := make(map[int]ClaimBinding, len(bindings))
	for _, binding := range bindings {
		bindingByIndex[binding.Index] = binding
	}

	var supported, behavioralUnverified, unchecked int
	var mismatchDetails []string
	boundFiles := make(map[string]bool)

	for _, claim := range tier2SortDecomposedByIndex(decomposed) {
		binding, found := bindingByIndex[claim.Index]
		if !found || !binding.Bound || binding.File == "" {
			continue // unbound — the caller routes `unanchored` (not this check's concern)
		}
		boundFiles[binding.File] = true

		if claim.Kind == DecomposedClaimKindOther {
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictUnchecked, otherDetail); err != nil {
				return schemas.VerificationCheck{}, err
			}
			unchecked++
			continue
		}
		if claim.Kind == DecomposedClaimKindBehavioral {
			if deps != nil && deps.ContractCorroborated != nil && deps.ContractCorroborated(claim) {
				if err := tracker.Set(claim.Index, schemas.ClaimVerdictSupported, corroboratedDetail); err != nil {
					return schemas.VerificationCheck{}, err
				}
				supported++
			} else {
				if err := tracker.Set(claim.Index, schemas.ClaimVerdictBehavioralUnverified, behavioralUnverifiedDetail); err != nil {
					return schemas.VerificationCheck{}, err
				}
				behavioralUnverified++
			}
			continue
		}

		at := binding.File
		if binding.Line != nil {
			at = fmt.Sprintf("%s:%d", binding.File, *binding.Line)
		}
		content, present := workspaceSources[binding.File]
		if !present {
			detail := fmt.Sprintf(
				"bound source file %s not present in the workspace (declared gap, A4 posture)",
				binding.File)
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictUnchecked, detail); err != nil {
				return schemas.VerificationCheck{}, err
			}
			unchecked++
			continue
		}

		tokens := ExtractClaimedTokens(claim.Text, claim.Kind)
		if len(tokens) == 0 {
			detail := fmt.Sprintf(
				"%s: no deterministic claimed token(s) extracted — recorded, not verified against %s",
				tier2ClaimIDFor(claim), at)
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictUnchecked, detail); err != nil {
				return schemas.VerificationCheck{}, err
			}
			unchecked++
			continue
		}

		evidence := tier2EvidenceWindow(content, binding.Line)
		var matched bool
		if claim.Kind == DecomposedClaimKindSignature {
			normalizedEvidence := tier2WithoutWhitespace(evidence)
			for _, token := range tokens {
				if strings.Contains(normalizedEvidence, tier2WithoutWhitespace(token)) {
					matched = true
					break
				}
			}
		} else {
			for _, token := range tokens {
				if strings.Contains(evidence, token) {
					matched = true
					break
				}
			}
		}
		if matched {
			detail := fmt.Sprintf("verified against %s", at)
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictSupported, detail); err != nil {
				return schemas.VerificationCheck{}, err
			}
			supported++
		} else {
			quoted := make([]string, 0, len(tokens))
			for _, token := range tokens {
				quoted = append(quoted, fmt.Sprintf("`%s`", token))
			}
			detail := fmt.Sprintf("%s: expected token(s) %s not found in %s",
				tier2ClaimIDFor(claim), strings.Join(quoted, ", "), at)
			if err := tracker.Set(claim.Index, schemas.ClaimVerdictMismatched, detail); err != nil {
				return schemas.VerificationCheck{}, err
			}
			mismatchDetails = append(mismatchDetails, detail)
		}
	}

	// inputs: sorted deduped `<file>@<sha>` entries from sourceHeadShas for the
	// bound files (invariant 10 — provenance or it didn't happen).
	files := make([]string, 0, len(boundFiles))
	for file := range boundFiles {
		files = append(files, file)
	}
	sort.Strings(files)
	var inputs []string
	for _, file := range files {
		if sha, ok := sourceHeadShas[file]; ok {
			inputs = append(inputs, fmt.Sprintf("%s@%s", file, sha))
		}
	}

	if len(mismatchDetails) > 0 {
		return schemas.VerificationCheck{
			ID:     "head-verification",
			Status: schemas.CheckFail,
			Detail: tier2StringPtr(fmt.Sprintf("%d claim(s) mismatched against the head: %s",
				len(mismatchDetails), strings.Join(mismatchDetails, "; "))),
			Inputs: inputs,
		}, nil
	}
	return schemas.VerificationCheck{
		ID:     "head-verification",
		Status: schemas.CheckPass,
		Detail: tier2StringPtr(fmt.Sprintf("%d supported, %d behavioral-unverified (OD-R09), %d unchecked (declared gaps)",
			supported, behavioralUnverified, unchecked)),
		Inputs: inputs,
	}, nil
}
