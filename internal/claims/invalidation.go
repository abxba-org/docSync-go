package claims

// Ported from core/src/analysis/claim-invalidation.ts (P11-T06; ARCH
// §4.1/§4.3/§6.2; CTX §11) into internal/claims because this module is pure
// wiring OVER the claim state machine exported by this package — the
// orchestrator assigned the invalidation test port to this unit and
// internal/analysis does not exist yet (plan.md §2). The port keeps the TS
// module's structure verbatim; it may be re-homed to internal/analysis when
// that package lands (the only import of this package is the store + the
// transition functions).
//
// Claim-level invalidation rules: the ZERO-LLM deterministic mapping from
// symbol-level evidence to proposed claim transitions. A claim bound to a
// symbol that changed at the type or signature level loses verification
// support (verified → suspect); visibility changes → stale; added/removed
// symbols → stale; untouched symbols → the claim is EXPLICITLY unchanged
// (the report shows why every claim moved or didn't — PRIN-6: claims backed
// by sources). NO model port exists here — the entry gate is deterministic
// evidence before AI (PRIN-3).
//
// Status vocabulary: the module speaks the PLAN's status names, which map
// IDENTITY onto the claim-store vocabulary (schemas.ClaimStatus*:
// 'verified'|'suspect'|'unanchored'|'stale'|'closed'). ApplyClaimInvalidations
// routes proposed statuses through the claim state machine's OWN transition
// functions (LoseSupport, MarkStale) — never a side-door write.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ClaimInvalidationRules is the versioned rule table: symbol change kind (or
// added/removed evidence) → proposed claim status.
var ClaimInvalidationRules = map[string]string{
	"type":       schemas.ClaimStatusSuspect,
	"signature":  schemas.ClaimStatusSuspect,
	"visibility": schemas.ClaimStatusStale,
	"added":      schemas.ClaimStatusStale,
	"removed":    schemas.ClaimStatusStale,
}

// ClaimInvalidationRuleOrder is the rule-TABLE precedence order (deterministic
// by rule priority, not by accidental evidence-array order):
// type > signature > visibility > added/removed. Go maps are unordered, so the
// TS Object.keys(CLAIM_INVALIDATION_RULES) order is pinned here.
var ClaimInvalidationRuleOrder = []string{"type", "signature", "visibility", "added", "removed"}

// ClaimInvalidationTransitions is the exported versioned dispatch table naming
// the machine's own transition functions: it documents the ONLY dispatch the
// wiring owns. There is NO side-door write — a claim status changes
// exclusively through those functions. Any other proposed status — notably
// 'verified' (re-verification belongs to the verifier, never to invalidation)
// and 'unanchored'/'closed' (owned by the anchor/retirement paths) — is
// REFUSED and reported as a typed rejection (INV-6-adjacent: a refused
// invalidation is surfaced, never silently dropped, never forced through a
// generic write).
var ClaimInvalidationTransitions = map[string]string{
	"suspect": "loseSupport",
	"stale":   "markStale",
}

// claimInvalidationTransitionOrder pins the dispatch table's listing order
// (the TS Object.entries insertion order) for deterministic messages.
var claimInvalidationTransitionOrder = []string{"suspect", "stale"}

// SymbolBoundClaim is a claim bound to a source symbol (the claim store's
// binding shape, status-agnostic).
type SymbolBoundClaim struct {
	ClaimID string
	Symbol  string
	DocPath string
	// Status is the claim's CURRENT status in the caller's vocabulary (opaque
	// to this module).
	Status string
}

// SymbolChangeEvidence is one changed symbol's evidence: the kinds are NAMES
// (not just schemas.SymbolChangeKind values — the added/removed markers are
// evidence names the rule table keys on, never schema enum members).
type SymbolChangeEvidence struct {
	Name        string
	ChangeKinds []string
}

// ClaimInvalidation is one proposed claim transition.
type ClaimInvalidation struct {
	ClaimID        string
	Symbol         string
	DocPath        string
	PreviousStatus string
	// ProposedStatus is the proposed target status per
	// ClaimInvalidationRules — applied via the EXISTING state machine.
	ProposedStatus string
	// Evidence is the symbol-level evidence: which change kinds triggered this.
	Evidence []SymbolChangeEvidence
	Reason   string
}

// ClaimUnchanged is a claim the invalidation left alone, with the reason.
type ClaimUnchanged struct {
	ClaimID        string
	Symbol         string
	DocPath        string
	PreviousStatus string
	Reason         string // "symbol-unchanged" | "symbol-not-present"
}

// ClaimInvalidationReport is the deterministic invalidation outcome.
type ClaimInvalidationReport struct {
	Invalidated []ClaimInvalidation
	Unchanged   []ClaimUnchanged
}

// DeriveClaimInvalidations is the deterministic zero-LLM invalidation over
// symbol-level evidence. A claim bound to MULTIPLE changed symbols takes the
// FIRST rule match in RULE-TABLE PRECEDENCE ORDER (type > signature >
// visibility), never the evidence order (deterministic; the report lists all
// triggering evidence).
func DeriveClaimInvalidations(symbolsDetail schemas.SymbolsDetail, claims []SymbolBoundClaim) ClaimInvalidationReport {
	changedByName := map[string][]string{}
	for _, change := range symbolsDetail.Changed {
		changedByName[change.Name] = changeKindNames(change.ChangeKinds)
	}
	removedSet := stringSet(symbolsDetail.Removed)
	addedSet := stringSet(symbolsDetail.Added)

	invalidated := make([]ClaimInvalidation, 0)
	unchanged := make([]ClaimUnchanged, 0)

	for _, claim := range claims {
		if changeKinds, ok := changedByName[claim.Symbol]; ok && len(changeKinds) > 0 {
			if reason := firstRuleMatch(changeKinds); reason != "" {
				invalidated = append(invalidated, ClaimInvalidation{
					ClaimID:        claim.ClaimID,
					Symbol:         claim.Symbol,
					DocPath:        claim.DocPath,
					PreviousStatus: claim.Status,
					ProposedStatus: ClaimInvalidationRules[reason],
					Evidence:       []SymbolChangeEvidence{{Name: claim.Symbol, ChangeKinds: changeKinds}},
					Reason:         reason,
				})
				continue
			}
		}
		if removedSet[claim.Symbol] {
			invalidated = append(invalidated, ClaimInvalidation{
				ClaimID:        claim.ClaimID,
				Symbol:         claim.Symbol,
				DocPath:        claim.DocPath,
				PreviousStatus: claim.Status,
				ProposedStatus: ClaimInvalidationRules["removed"],
				Evidence:       []SymbolChangeEvidence{{Name: claim.Symbol, ChangeKinds: []string{"removed"}}},
				Reason:         "removed",
			})
			continue
		}
		if addedSet[claim.Symbol] {
			invalidated = append(invalidated, ClaimInvalidation{
				ClaimID:        claim.ClaimID,
				Symbol:         claim.Symbol,
				DocPath:        claim.DocPath,
				PreviousStatus: claim.Status,
				ProposedStatus: ClaimInvalidationRules["added"],
				Evidence:       []SymbolChangeEvidence{{Name: claim.Symbol, ChangeKinds: []string{"added"}}},
				Reason:         "added",
			})
			continue
		}
		reason := "symbol-not-present"
		if _, ok := changedByName[claim.Symbol]; ok {
			reason = "symbol-unchanged"
		}
		unchanged = append(unchanged, ClaimUnchanged{
			ClaimID:        claim.ClaimID,
			Symbol:         claim.Symbol,
			DocPath:        claim.DocPath,
			PreviousStatus: claim.Status,
			Reason:         reason,
		})
	}
	return ClaimInvalidationReport{Invalidated: invalidated, Unchanged: unchanged}
}

// firstRuleMatch returns the first rule in table precedence order present in
// changeKinds ("" when none matches).
func firstRuleMatch(changeKinds []string) string {
	for _, rule := range ClaimInvalidationRuleOrder {
		for _, kind := range changeKinds {
			if kind == rule {
				return rule
			}
		}
	}
	return ""
}

// changeKindNames projects schema change kinds onto their wire names.
func changeKindNames(kinds []schemas.SymbolChangeKind) []string {
	names := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		names = append(names, string(kind))
	}
	return names
}

// stringSet builds a membership set.
func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

/* ------------------------------------------------- state-machine wiring (F1b) */

// AppliedClaimInvalidation is a transition the machine accepted and wrote
// (CAS-guarded, history appended).
type AppliedClaimInvalidation struct {
	ClaimID string
	Symbol  string
	DocPath string
	From    string
	To      string
	Reason  string
	// Event is the history event the machine appended
	// (`support lost: …` / `stale: …`).
	Event string
	// Hash is the new CAS hash of the claim after the machine's write.
	Hash string
}

// RejectedClaimInvalidation is a proposed status the machine has no owned
// transition function for — refused, reported, never written.
type RejectedClaimInvalidation struct {
	ClaimID string
	Symbol  string
	DocPath string
	From    string
	// To is the refused proposed status, verbatim.
	To     string
	Reason string // "no-transition-function"
	Detail string
}

// ConflictedClaimInvalidation is a CAS conflict surfaced by the machine's own
// conflict shape — the loser re-runs, never overwrites (R15).
type ConflictedClaimInvalidation struct {
	ClaimID      string
	From         string
	To           string
	ExpectedHash *string
	ActualHash   *string
}

// ClaimInvalidationApplyReport is the fully reported apply outcome.
type ClaimInvalidationApplyReport struct {
	Applied   []AppliedClaimInvalidation
	Conflicts []ConflictedClaimInvalidation
	Rejected  []RejectedClaimInvalidation
}

// ClaimInvalidationApplyInput is the apply input bag.
type ClaimInvalidationApplyInput struct {
	InvalidationReport ClaimInvalidationReport
	Store              *ClaimStore
	RunID              string
	// At is the ISO-8601 timestamp of the transitions (caller-owned; no
	// hidden clock).
	At string
	// ExpectedHashByClaim carries optional per-claim CAS expectations (a
	// stale hash surfaces a conflict, per the machine).
	ExpectedHashByClaim map[string]string
}

// describeEvidence is the one-line deterministic evidence description,
// embedded in the machine's history event.
func describeEvidence(entry ClaimInvalidation) string {
	parts := make([]string, 0, len(entry.Evidence))
	for _, item := range entry.Evidence {
		parts = append(parts, fmt.Sprintf("%s (%s)", item.Name, strings.Join(item.ChangeKinds, ", ")))
	}
	return fmt.Sprintf("symbol evidence: %s [%s]", strings.Join(parts, "; "), entry.Reason)
}

// lastHistoryEvent is the machine's just-appended history event
// (`history.at(-1)?.event ?? ”`).
func lastHistoryEvent(claim *schemas.Claim) string {
	if claim == nil || len(claim.History) == 0 {
		return ""
	}
	return claim.History[len(claim.History)-1].Event
}

// ApplyClaimInvalidations applies a DeriveClaimInvalidations report through
// the EXISTING claim state machine: each invalidated entry is dispatched by
// its proposed status to the machine's own transition function, which performs
// the CAS-guarded status write and appends the history event. Unchanged claims
// are never written. Outcomes are fully reported: Applied (machine wrote),
// Conflicts (the machine's CAS conflict shape — re-run the caller), Rejected
// (no owned machine function for the proposed status — INV-6-adjacent,
// surfaced not dropped). Timestamps/owner are caller-owned (no hidden clock).
func ApplyClaimInvalidations(input ClaimInvalidationApplyInput) (ClaimInvalidationApplyReport, error) {
	applied := make([]AppliedClaimInvalidation, 0)
	conflicts := make([]ConflictedClaimInvalidation, 0)
	rejected := make([]RejectedClaimInvalidation, 0)

	for _, entry := range input.InvalidationReport.Invalidated {
		var expectedHash *string
		if hash, ok := input.ExpectedHashByClaim[entry.ClaimID]; ok {
			expectedHash = &hash
		}
		options := ClaimTransitionOptions{
			RunID:        input.RunID,
			At:           input.At,
			Reason:       describeEvidence(entry),
			ExpectedHash: expectedHash,
		}

		// The ONLY way a claim status changes: the machine's own transition
		// functions.
		var write ClaimTransitionResult
		var err error
		switch entry.ProposedStatus {
		case schemas.ClaimStatusSuspect:
			write, err = LoseSupport(input.Store, entry.ClaimID, options)
		case schemas.ClaimStatusStale:
			write, err = MarkStale(input.Store, entry.ClaimID, options)
		default:
			ownedParts := make([]string, 0, len(claimInvalidationTransitionOrder))
			for _, status := range claimInvalidationTransitionOrder {
				ownedParts = append(ownedParts,
					fmt.Sprintf("'%s' → %s()", status, ClaimInvalidationTransitions[status]))
			}
			rejected = append(rejected, RejectedClaimInvalidation{
				ClaimID: entry.ClaimID,
				Symbol:  entry.Symbol,
				DocPath: entry.DocPath,
				From:    entry.PreviousStatus,
				To:      entry.ProposedStatus,
				Reason:  "no-transition-function",
				Detail: fmt.Sprintf(
					"no invalidation-owned machine transition for proposed status '%s' — the wiring dispatches only %s through the claim state machine; the status is NOT written and the refusal is reported",
					entry.ProposedStatus, strings.Join(ownedParts, ", ")),
			})
			continue
		}
		if err != nil {
			return ClaimInvalidationApplyReport{}, err
		}

		if write.OK {
			applied = append(applied, AppliedClaimInvalidation{
				ClaimID: entry.ClaimID,
				Symbol:  entry.Symbol,
				DocPath: entry.DocPath,
				From:    entry.PreviousStatus,
				To:      entry.ProposedStatus,
				Reason:  entry.Reason,
				Event:   lastHistoryEvent(write.Claim),
				Hash:    write.Hash,
			})
			continue
		}
		conflicts = append(conflicts, ConflictedClaimInvalidation{
			ClaimID:      entry.ClaimID,
			From:         entry.PreviousStatus,
			To:           entry.ProposedStatus,
			ExpectedHash: write.ExpectedHash,
			ActualHash:   write.ActualHash,
		})
	}
	return ClaimInvalidationApplyReport{Applied: applied, Conflicts: conflicts, Rejected: rejected}, nil
}
