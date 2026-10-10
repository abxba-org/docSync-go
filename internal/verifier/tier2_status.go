package verifier

// Tier-2 claim-status tracker (port of tier2/status.ts; P4-T08..T12; PLAN
// §5.4; ARCH §4.8 Tier-2): the per-claim status surface of the verification
// report. Per-claim statuses are the CANONICAL auto-eligibility input —
// schemas.ResolveAutoEligibility (wave-1 schema, PLAN §5.3 / OD-R09) consumes
// exactly this array — so the tracker never decides eligibility itself: it only
// records statuses with fail-closed validation. `unanchored` and
// `behavioral-unverified` clear auto-eligibility downstream (unanchored ⇒
// suggest, never auto — PLAN §5.6/ARCH §4.8).
//
// Determinism: a decomposed claim's `claim_id` is its `existing_claim_id` when
// present, else the stable fallback `decomposed-<index>`; snapshots always
// cover every decomposed claim in `index` order with `unchecked` as the
// never-set default, so records are replayable evidence (invariant 10).

import (
	"fmt"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// trackerEntry is one decomposed claim's tracker key (index order).
type trackerEntry struct {
	index   int
	claimID string
}

// settledStatus is one recorded per-claim status; a nil detail is an absent
// optional field (TS: `detail === undefined ? { status } : { status, detail }`).
type settledStatus struct {
	status string
	detail *string
}

// ClaimStatusTracker is a per-claim status accumulator over one Tier-2 run's
// decomposed claims. It is single-goroutine state (one Tier-2 run records its
// own statuses sequentially); nothing shares a tracker across goroutines.
type ClaimStatusTracker struct {
	entries []trackerEntry
	indices map[int]bool
	settled map[int]settledStatus
}

/* ---------------------------------------------------------------- exports */

// CreateClaimStatusTracker builds the tracker over the run's decomposed claims.
// Fail-closed (§3 rule 6): a duplicate claim index is a hard error — the TS
// `throw new Error('duplicate decomposed-claim index …')` surfaces as a coded
// docutil error, never a silent no-op.
func CreateClaimStatusTracker(decomposed []DecomposedClaim) (*ClaimStatusTracker, error) {
	sorted := tier2SortDecomposedByIndex(decomposed)
	entries := make([]trackerEntry, 0, len(sorted))
	indices := make(map[int]bool, len(sorted))
	for _, claim := range sorted {
		if indices[claim.Index] {
			return nil, docutil.New(docutil.CodeValidation, fmt.Sprintf(
				"duplicate decomposed-claim index %d (fail-closed)", claim.Index))
		}
		indices[claim.Index] = true
		entries = append(entries, trackerEntry{index: claim.Index, claimID: tier2ClaimIDFor(claim)})
	}
	return &ClaimStatusTracker{
		entries: entries,
		indices: indices,
		settled: map[int]settledStatus{},
	}, nil
}

// Set records `status` for decomposed claim `index` (last write wins). The
// optional trailing detail carries the deterministic evidence detail; passing
// no detail leaves the record's `detail` absent, exactly as the TS
// `detail === undefined` branch. Fail-closed: an unknown claim index or an
// invalid status is a coded error (the TS throws), never a degraded default.
func (t *ClaimStatusTracker) Set(index int, status string, detail ...string) error {
	if !t.indices[index] {
		return docutil.New(docutil.CodeValidation, fmt.Sprintf(
			"unknown decomposed-claim index %d (fail-closed)", index))
	}
	// The TS VERIFICATION_CLAIM_STATUS.parse(status) zod failure, reproduced
	// through the shared enum validator so the message text matches the
	// schema suites.
	if issue := schemas.CheckEnum(status, schemas.VerificationClaimStatusValues, []string{"status"}); issue != nil {
		return docutil.New(docutil.CodeValidation, fmt.Sprintf(
			"invalid verification claim status: %s (fail-closed)", issue.Message))
	}
	settled := settledStatus{status: status}
	if len(detail) > 0 {
		settled.detail = tier2StringPtr(detail[0])
	}
	t.settled[index] = settled
	return nil
}

// Snapshot returns the claims in `index` order; never-set claims default to
// `unchecked`.
func (t *ClaimStatusTracker) Snapshot() []schemas.VerificationClaim {
	snapshot := make([]schemas.VerificationClaim, 0, len(t.entries))
	for _, entry := range t.entries {
		recorded, ok := t.settled[entry.index]
		if !ok {
			snapshot = append(snapshot, schemas.VerificationClaim{
				ClaimID: entry.claimID,
				Status:  schemas.ClaimVerdictUnchecked,
			})
			continue
		}
		snapshot = append(snapshot, schemas.VerificationClaim{
			ClaimID: entry.claimID,
			Status:  recorded.status,
			Detail:  recorded.detail,
		})
	}
	return snapshot
}
