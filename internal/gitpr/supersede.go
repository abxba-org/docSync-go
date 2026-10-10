package gitpr

// Ported from core/src/gitpr/supersede.ts (P6-T13; PLAN §7.3 "Rate, burst,
// idempotency, lifecycle"; ARCH §12.2): stale-PR supersede flow.
//
// When the source PR evolves after a doc PR opens, the idempotent re-run must
// not duplicate: the stale doc PR is marked "superseded by newer analysis" and
// replaced; a legitimate re-creation after a revert gets a deterministic
// disambiguator suffix. Three pieces:
//
//  1. SupersedeStalePr — appends the EXACT marker comment
//     (SupersedeMarker: `superseded by newer analysis (#<n>)`), then hands the
//     lifecycle transition to the client's MarkSuperseded. Idempotent by
//     marker: the comment thread is read FIRST and an already present marker is
//     a no-op — re-invocation never duplicates.
//  2. DisambiguatedBranchName — the pure name-level half of the branch
//     factory's deterministic scan (P6-T13 rule): `<name>`, `<name>-2`,
//     `<name>-3`, … FIRST FREE candidate, same bound
//     (MAX_DISAMBIGUATOR_CANDIDATES) and the same typed error on exhaustion
//     as the factory's createDocBranch. Mirrored here because the factory's
//     scan is git-bound (tip checks); a test pins the candidate-order equality
//     between the two.
//  3. EvaluateSupersedence — the pure decision matrix the orchestrator applies
//     before any PR write: same idempotency key → `update` the existing PR;
//     different key with an existing OPEN PR → `supersede-then-create`; no
//     existing open PR → `create`. CONTRACT: the existing lookup is marker-keyed
//     over OPEN doc PRs only — a closed/merged prior PR is passed as nil (there
//     is nothing to supersede; a new key legitimately creates a new PR).
//
// All operations are deterministic given the client's answers; the clock is
// injected and only stamps results (P19).

import (
	"context"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// EDOCSYNCSupersede preserves the TS static `SupersedeError.code`
// (EDOCSYNC_SUPERSEDE), which internal/docutil does not list.
const EDOCSYNCSupersede = "EDOCSYNC_SUPERSEDE"

// SupersedeError is the fail-closed refusal of a malformed supersede input
// (typed, fail loud).
type SupersedeError struct {
	Msg string
}

// Error reproduces the TS message text.
func (e *SupersedeError) Error() string { return e.Msg }

// Code exposes the machine-readable failure class (TS: static/instance `code`).
func (e *SupersedeError) Code() string { return EDOCSYNCSupersede }

func newSupersedeError(msg string) *SupersedeError { return &SupersedeError{Msg: msg} }

// SupersedeMarkerPrefix is the marker prefix — the supersede note MUST start
// with this exact text.
const SupersedeMarkerPrefix = "superseded by newer analysis (#"

// SupersedeMarker is the exact supersede marker comment for a newer PR number:
// `superseded by newer analysis (#<n>)`.
func SupersedeMarker(newerPrNumber int) (string, error) {
	if newerPrNumber < 1 {
		return "", newSupersedeError(
			"newerPrNumber must be a positive integer (got " + strconv.Itoa(newerPrNumber) + ")")
	}
	return SupersedeMarkerPrefix + strconv.Itoa(newerPrNumber) + ")", nil
}

// SupersedePrClient is the structural seam for SupersedeStalePr: the minimal
// PR-client surface the supersede flow needs — read the comment thread (the
// idempotency check), append the marker, run the lifecycle transition.
// `PrPlatformPort` implementations satisfy this with a thin adapter over their
// comment accessors; the Phase-8 REST transport implements it directly. No
// credentials here (INV-8 — the transport closes over them).
//
// TODO(seam:pr-platform): the SeamPrRef parameter becomes pr-platform.go's PrRef
// when that file lands.
type SupersedePrClient interface {
	ListPrComments(ctx context.Context, pr SeamPrRef) ([]string, error)
	AppendPrComment(ctx context.Context, pr SeamPrRef, body string) error
	MarkSuperseded(ctx context.Context, pr SeamPrRef, newerPr SeamPrRef) error
}

// SupersedeStalePrInput is the input of SupersedeStalePr.
type SupersedeStalePrInput struct {
	// Stale is the stale doc PR to supersede (the older analysis' PR).
	Stale SeamPrRef
	// Newer is the newer re-run's PR that replaces it.
	Newer SeamPrRef
	// Clock stamps the RESULT only — the marker text is exact.
	Clock func() string
}

// Supersede actions (TS: the closed SupersedeResult union's `action`).
const (
	SupersedeActionSuperseded = "superseded"
	SupersedeActionAlready    = "already-superseded"
)

// SupersedeResult is the result of SupersedeStalePr.
type SupersedeResult struct {
	Action string `json:"action"`
	At     string `json:"at"`
}

// SupersedeStalePr marks `stale` superseded by `newer` (PLAN §7.3): appends the
// exact marker comment, then MarkSuperseded. IDEMPOTENT: the comment thread is
// read first and a present marker short-circuits to a no-op — re-invocation
// after a partial or repeated run never duplicates the marker. Fails loud on
// malformed input; platform failures propagate (fail closed).
func SupersedeStalePr(client SupersedePrClient, input SupersedeStalePrInput) (SupersedeResult, error) {
	if input.Clock == nil {
		return SupersedeResult{}, newSupersedeError(
			"supersedeStalePr: clock must be a () => string function")
	}
	for _, entry := range []struct {
		label string
		pr    SeamPrRef
	}{
		{"stale", input.Stale},
		{"newer", input.Newer},
	} {
		if entry.pr.Number < 1 {
			return SupersedeResult{}, newSupersedeError(
				"supersedeStalePr: " + entry.label + ".number must be a positive integer")
		}
	}
	if input.Stale.Number == input.Newer.Number {
		return SupersedeResult{}, newSupersedeError(
			"supersedeStalePr: a PR cannot supersede itself (#" +
				strconv.Itoa(input.Stale.Number) + ")")
	}
	marker, err := SupersedeMarker(input.Newer.Number)
	if err != nil {
		return SupersedeResult{}, err
	}
	comments, err := client.ListPrComments(context.Background(), input.Stale)
	if err != nil {
		return SupersedeResult{}, err
	}
	for _, comment := range comments {
		if comment == marker {
			return SupersedeResult{
				Action: SupersedeActionAlready,
				At:     input.Clock(),
			}, nil
		}
	}
	if err := client.AppendPrComment(context.Background(), input.Stale, marker); err != nil {
		return SupersedeResult{}, err
	}
	if err := client.MarkSuperseded(context.Background(), input.Stale, input.Newer); err != nil {
		return SupersedeResult{}, err
	}
	return SupersedeResult{
		Action: SupersedeActionSuperseded,
		At:     input.Clock(),
	}, nil
}

// DisambiguatedBranchName returns the FIRST FREE candidate in the branch
// factory's deterministic order `<base>`, `<base>-2`, `<base>-3`, … (P6-T13
// re-creation-after-revert rule). Pure name-level mirror of the factory's
// scan: same candidate formula, same SeamMaxDisambiguatorCandidates bound, same
// typed error on exhaustion (never a silent fallback).
func DisambiguatedBranchName(base string, taken map[string]bool) (string, error) {
	if base == "" {
		return "", branchNameError("disambiguatedBranchName: base must be a nonempty string")
	}
	for attempt := 0; attempt < MAX_DISAMBIGUATOR_CANDIDATES; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = base + "-" + strconv.Itoa(attempt+1)
		}
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", branchNameError(
		"no free branch name for '" + base + "' after " +
			strconv.Itoa(MAX_DISAMBIGUATOR_CANDIDATES) +
			" deterministic disambiguator candidates")
}

// Supersedence actions (TS: the closed SupersedenceDecision union's `action`).
const (
	SupersedenceActionCreate            = "create"
	SupersedenceActionUpdate            = "update"
	SupersedenceActionSupersedeThenCrea = "supersede-then-create"
)

// SupersedenceDecision is the supersede decision (PLAN §7.3 stale-doc-PR
// lifecycle).
type SupersedenceDecision struct {
	Action           string `json:"action"`
	ExistingPrNumber int    `json:"existing_pr_number,omitempty"`
}

// SupersedenceExistingPr is the EXISTING open doc PR's marker lookup —
// `{prNumber, idempotencyKey}` when an OPEN doc PR exists (any key). A nil
// *SupersedenceExistingPr means none does (a closed/merged prior PR is passed
// as nil: nothing to supersede).
type SupersedenceExistingPr struct {
	PrNumber       int    `json:"pr_number"`
	IdempotencyKey string `json:"idempotency_key"`
}

// EvaluateSupersedence is the pure decision matrix (P6-T13): same idempotency
// key → `update` (the marker-keyed upsert path); different key with an existing
// OPEN PR → `supersede-then-create` (stale doc PR); no existing open PR →
// `create`. Fail closed on malformed input — never a guessed action.
func EvaluateSupersedence(existing *SupersedenceExistingPr, incomingKey string) (SupersedenceDecision, error) {
	if incomingKey == "" {
		return SupersedenceDecision{}, newSupersedeError(
			"evaluateSupersedence: incomingKey must be a nonempty string")
	}
	if existing == nil {
		return SupersedenceDecision{Action: SupersedenceActionCreate}, nil
	}
	if existing.PrNumber < 1 {
		return SupersedenceDecision{}, newSupersedeError(
			"evaluateSupersedence: existing.prNumber must be a positive integer (got " +
				strconv.Itoa(existing.PrNumber) + ")")
	}
	if existing.IdempotencyKey == "" {
		return SupersedenceDecision{}, newSupersedeError(
			"evaluateSupersedence: existing.idempotencyKey must be nonempty")
	}
	if existing.IdempotencyKey == incomingKey {
		return SupersedenceDecision{
			Action:           SupersedenceActionUpdate,
			ExistingPrNumber: existing.PrNumber,
		}, nil
	}
	return SupersedenceDecision{
		Action:           SupersedenceActionSupersedeThenCrea,
		ExistingPrNumber: existing.PrNumber,
	}, nil
}

// branchNameError is the typed exhaustion/invalid-base refusal. TS threw
// branch-factory.js's BranchNameError; the landed branch-factory.go owns that
// type (and its unexported constructor), so this seam preserves the class
// IDENTITY through the same CodeBranchName code the type reports.
//
// TODO(seam:branch-factory): return *BranchNameError once that file exports a
// constructor (or move disambiguatedBranchName's helper next to the factory).
func branchNameError(msg string) error {
	return docutil.New(CodeBranchName, msg)
}
