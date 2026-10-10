package verifier

// Tier-2 contract-aware checking (port of tier2/contract-check.ts; P4-T13;
// PLAN §5.4 item 13; ARCH §4.4 + §4.8 Tier-2): the removed region may not
// document a declared contract whose classification signal HIT — deleting
// contract language while the contract's code signal survives is a contract
// violation, rejected at tier 2 (V-P4-9).
//
// Two deterministic surfaces over the declared contract names (graph mapping
// order) and the P1-T12 classification vector:
//
//  1. Violation scan — any hunk's `delete_text` mentioning a declared contract
//     name (case-insensitive, word-boundary regex) whose vector entry has
//     `hit: true` fails the check. An `abstain: true` (or missing vector) entry
//     on a mentioned contract SKIPS the check with an explicit record — never
//     silently passed (§3 rule 9: fail closed, degrade to less automation).
//     Absent/`hit: false` entries are clean.
//  2. Corroboration map — decomposed claims of kind 'behavioral' whose text
//     word-overlaps (≥1 shared word of ≥4 chars, case-insensitive) a
//     'behavior'-kind declared contract with `hit: true`. Their indices are
//     returned as `corroborated`, the `ContractCorroborated` input of
//     head-verify (P4-T10): corroborated behavioral predicates may record
//     'supported'; everything else abstains (OD-R09).
//
// The `tracker` parameter is accepted for call-site uniformity across the
// tier-2 checks but deliberately UNUSED here: this check owns no per-claim
// status — corroboration flows through head-verify's port, which alone grades
// the affected claims.
//
// Pure and deterministic (module doc): no fs, no git, no network, no clock of
// its own — the same inputs always yield deep-equal records.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ContractAwareResult is the composed result: the check record plus the
// corroboration index set.
type ContractAwareResult struct {
	// Check is the contract-aware-check record (closed check-id vocabulary).
	Check schemas.VerificationCheck `json:"check"`
	// Corroborated holds the decomposed-claim indices contract-corroborated
	// (head-verify's port input).
	Corroborated map[int]bool `json:"corroborated"`
}

// mentionedContract is one mentioned contract with its (possibly absent)
// vector entry.
type mentionedContract struct {
	name  string
	entry *contracts.ContractVectorEntry
}

// significantWordRE matches the lowercase word tokens used by the
// corroboration comparison surface.
var significantWordRE = regexp.MustCompile("[a-z0-9]+")

/* ------------------------------------------------------------------ helpers */

// tier2MentionsContract reports whether `text` mentions `name` —
// case-insensitive, word-bounded, so a name like `refund-idempotency` is not
// matched inside unrelated longer words.
func tier2MentionsContract(text string, name string) bool {
	pattern := `\b` + regexp.QuoteMeta(name) + `\b`
	return regexp.MustCompile("(?i)" + pattern).MatchString(text)
}

// tier2SignificantWords is the lowercase word tokens of ≥4 chars — the
// corroboration comparison surface.
func tier2SignificantWords(text string) []string {
	words := significantWordRE.FindAllString(strings.ToLower(text), -1)
	significant := make([]string, 0, len(words))
	for _, word := range words {
		if len(word) >= 4 {
			significant = append(significant, word)
		}
	}
	return significant
}

// tier2SharesWord reports whether two token lists share ≥1 word (the
// corroboration overlap test).
func tier2SharesWord(left []string, right []string) bool {
	rightSet := make(map[string]bool, len(right))
	for _, word := range right {
		rightSet[word] = true
	}
	for _, word := range left {
		if rightSet[word] {
			return true
		}
	}
	return false
}

/* ---------------------------------------------------------------- exports */

// CheckContractAware runs the contract-aware check over the patch's removed
// regions and builds the corroboration map for head-verify. See the module doc
// for the two surfaces. All outcomes emit one record; inapplicable inputs skip
// WITH detail — never silent. Pure and deterministic (module doc).
//
// The tracker argument is accepted for call-site uniformity and deliberately
// unused (module doc).
func CheckContractAware(input Tier2Input, _ *ClaimStatusTracker) ContractAwareResult {
	emptyCorroborated := map[int]bool{}
	contractNames := input.ContractNames
	if len(contractNames) == 0 {
		return ContractAwareResult{
			Check: schemas.VerificationCheck{
				ID:     "contract-aware-check",
				Status: schemas.CheckSkipped,
				Detail: tier2StringPtr("no declared contracts for this document"),
			},
			Corroborated: emptyCorroborated,
		}
	}

	var vectorEntries []contracts.ContractVectorEntry
	if input.ContractVector != nil {
		vectorEntries = input.ContractVector.Vectors
	}
	entryFor := func(name string) *contracts.ContractVectorEntry {
		for i := range vectorEntries {
			if vectorEntries[i].Contract == name {
				return &vectorEntries[i]
			}
		}
		return nil
	}
	kindFor := func(name string, entry *contracts.ContractVectorEntry) schemas.ContractKind {
		if kind, ok := input.ContractKinds[name]; ok {
			return kind
		}
		if entry != nil {
			return entry.Kind
		}
		if input.Graph != nil {
			if kind, ok := input.Graph.ContractKinds[name]; ok {
				return kind
			}
		}
		return ""
	}

	/* -- 1. violation scan over the removed regions -------------------------- */

	var mentioned []mentionedContract
	seenNames := make(map[string]bool, len(contractNames))
	if input.Patch != nil {
		for hunkIndex := range input.Patch.Hunks {
			hunk := &input.Patch.Hunks[hunkIndex]
			if hunk.DeleteText == "" {
				continue
			}
			for _, name := range contractNames {
				if seenNames[name] {
					continue
				}
				if !tier2MentionsContract(hunk.DeleteText, name) {
					continue
				}
				seenNames[name] = true
				mentioned = append(mentioned, mentionedContract{name: name, entry: entryFor(name)})
			}
		}
	}

	var violations []mentionedContract
	for _, entry := range mentioned {
		if entry.entry != nil && !entry.entry.Abstain && entry.entry.Hit {
			violations = append(violations, entry)
		}
	}
	if len(violations) > 0 {
		details := make([]string, 0, len(violations))
		for _, violation := range violations {
			details = append(details, fmt.Sprintf(
				"contract violation: removed region documents declared contract %s (V-P4-9)",
				violation.name))
		}
		return ContractAwareResult{
			Check: schemas.VerificationCheck{
				ID:     "contract-aware-check",
				Status: schemas.CheckFail,
				Detail: tier2StringPtr(strings.Join(details, "; ")),
			},
			Corroborated: emptyCorroborated,
		}
	}

	var abstained []mentionedContract
	for _, entry := range mentioned {
		if entry.entry == nil || entry.entry.Abstain {
			abstained = append(abstained, entry)
		}
	}
	if len(abstained) > 0 {
		details := make([]string, 0, len(abstained))
		for _, entry := range abstained {
			details = append(details, fmt.Sprintf(
				"contract classification abstained for %s (never silently passed)", entry.name))
		}
		return ContractAwareResult{
			Check: schemas.VerificationCheck{
				ID:     "contract-aware-check",
				Status: schemas.CheckSkipped,
				Detail: tier2StringPtr(strings.Join(details, "; ")),
			},
			Corroborated: emptyCorroborated,
		}
	}

	/* -- 2. corroboration map for head-verify (OD-R09 escape hatch) ---------- */

	var behaviorContracts [][]string
	for _, name := range contractNames {
		entry := entryFor(name)
		if kindFor(name, entry) != schemas.ContractKindBehavior {
			continue
		}
		if entry == nil || entry.Abstain || !entry.Hit {
			continue
		}
		behaviorContracts = append(behaviorContracts, tier2SignificantWords(name))
	}

	corroborated := map[int]bool{}
	if len(behaviorContracts) > 0 {
		for _, claim := range DecomposePatchClaims(input.Patch) {
			if claim.Kind != DecomposedClaimKindBehavioral {
				continue
			}
			claimWords := tier2SignificantWords(claim.Text)
			for _, contractWords := range behaviorContracts {
				if tier2SharesWord(claimWords, contractWords) {
					corroborated[claim.Index] = true
					break
				}
			}
		}
	}

	deletionHunks := 0
	if input.Patch != nil {
		for hunkIndex := range input.Patch.Hunks {
			if input.Patch.Hunks[hunkIndex].DeleteText != "" {
				deletionHunks++
			}
		}
	}
	return ContractAwareResult{
		Check: schemas.VerificationCheck{
			ID:     "contract-aware-check",
			Status: schemas.CheckPass,
			Detail: tier2StringPtr(fmt.Sprintf(
				"no contract violations: %d declared contract(s) checked against %d deletion hunk(s), "+
					"%d behavioral claim(s) contract-corroborated",
				len(contractNames), deletionHunks, len(corroborated))),
		},
		Corroborated: corroborated,
	}
}
