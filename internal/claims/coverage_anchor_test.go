package claims_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/require"
)

type errBackend struct{}

func (e *errBackend) ReadClaim(string) (state.StateRead[*schemas.Claim], error) {
	return state.StateRead[*schemas.Claim]{}, errors.New("backend err")
}
func (e *errBackend) WriteClaim(string, schemas.Claim, *string) (state.CasWriteResult, error) {
	return state.CasWriteResult{}, errors.New("backend err")
}
func (e *errBackend) ListClaimIds() ([]string, error) {
	return nil, errors.New("backend err")
}

type getErrBackend struct{ ids []string }

func (g *getErrBackend) ReadClaim(string) (state.StateRead[*schemas.Claim], error) {
	return state.StateRead[*schemas.Claim]{}, errors.New("get err")
}
func (g *getErrBackend) WriteClaim(string, schemas.Claim, *string) (state.CasWriteResult, error) {
	return state.CasWriteResult{}, errors.New("err")
}
func (g *getErrBackend) ListClaimIds() ([]string, error) {
	return g.ids, nil
}

type writeErrBackend struct {
	claims []schemas.Claim
	hashes []string
}

func (w *writeErrBackend) ReadClaim(claimID string) (state.StateRead[*schemas.Claim], error) {
	for i, c := range w.claims {
		if c.ID == claimID {
			return state.StateRead[*schemas.Claim]{Content: &c, Hash: w.hashes[i], Exists: true}, nil
		}
	}
	return state.StateRead[*schemas.Claim]{}, nil
}
func (w *writeErrBackend) WriteClaim(_ string, _ schemas.Claim, _ *string) (state.CasWriteResult, error) {
	return state.CasWriteResult{}, errors.New("write err")
}
func (w *writeErrBackend) ListClaimIds() ([]string, error) { return nil, nil }

func TestHashOf_BackendError(t *testing.T) {
	store := claims.NewClaimStore(&errBackend{})
	_, err := store.HashOf("x")
	require.Error(t, err)
}

func TestGet_BackendError(t *testing.T) {
	store := claims.NewClaimStore(&errBackend{})
	_, err := store.Get("x")
	require.Error(t, err)
}

func TestList_BackendErrors(t *testing.T) {
	t.Run("list claim ids error", func(t *testing.T) {
		store := claims.NewClaimStore(&errBackend{})
		_, err := store.List()
		require.Error(t, err)
	})
	t.Run("list get error", func(t *testing.T) {
		store := claims.NewClaimStore(&getErrBackend{ids: []string{"c1"}})
		_, err := store.List()
		require.Error(t, err)
	})
}

func TestCloseClaim_ToClosed(t *testing.T) {
	store := newClaimStore(t)
	mustUpsert(t, store, makeClaim())
	result, err := claims.CloseClaim(store, "claim_1", claims.ClaimTransitionOptions{
		RunID: runID, Reason: "retired", At: t0,
	})
	require.NoError(t, err)
	require.True(t, result.OK)
	require.NotNil(t, result.Claim)
	require.Equal(t, schemas.ClaimStatusClosed, result.Claim.Status)
	require.Contains(t, lastEvent(result.Claim).Event, "closed: retired")
}

func TestTransition_NonExistentClaim(t *testing.T) {
	store := newClaimStore(t)
	_, err := claims.LoseSupport(store, "nonexistent", claims.ClaimTransitionOptions{
		RunID: runID, Reason: "x", At: t0,
	})
	require.Error(t, err)
}

func TestTransition_StoreReadError(t *testing.T) {
	store := claims.NewClaimStore(&errBackend{})
	_, err := claims.LoseSupport(store, "claim_1", claims.ClaimTransitionOptions{
		RunID: runID, Reason: "x", At: t0,
	})
	require.Error(t, err)
}

func TestTransition_CASConflict(t *testing.T) {
	store := newClaimStore(t)
	mustUpsert(t, store, makeClaim())
	h0, err := store.HashOf("claim_1")
	require.NoError(t, err)
	require.NotNil(t, h0)
	concurrent := makeClaim()
	concurrent.Status = schemas.ClaimStatusSuspect
	_, err = store.Upsert(concurrent, &claims.ClaimUpsertOptions{ExpectedHash: h0})
	require.NoError(t, err)
	result, err := claims.LoseSupport(store, "claim_1", claims.ClaimTransitionOptions{
		RunID: runID, Reason: "x", At: t0, ExpectedHash: h0,
	})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.NotNil(t, result.ExpectedHash)
	require.NotNil(t, result.ActualHash)
}

func TestApplyReanchor_NonexistentClaim(t *testing.T) {
	store := newClaimStore(t)
	resolution := claims.AnchorResolution{
		Status: claims.AnchorReAnchored, StartLine: 3, EndLine: 3,
		Previous: &claims.LineRange{StartLine: 2, EndLine: 2},
	}
	_, err := claims.ApplyReanchor(store, "nonexistent", resolution,
		claims.ApplyReanchorOptions{RunID: runID, At: t0})
	require.Error(t, err)
}

func TestApplyReanchor_ReadError(t *testing.T) {
	store := claims.NewClaimStore(&errBackend{})
	_, err := claims.ApplyReanchor(store, "x", claims.AnchorResolution{Status: claims.AnchorReAnchored},
		claims.ApplyReanchorOptions{RunID: runID, At: t0})
	require.Error(t, err)
}

func TestApplyReanchor_UpsertError(t *testing.T) {
	claim := makeClaim()
	store := claims.NewClaimStore(&writeErrBackend{
		claims: []schemas.Claim{claim},
		hashes: []string{"h0"},
	})
	resolution := claims.AnchorResolution{
		Status: claims.AnchorReAnchored, StartLine: 3, EndLine: 3,
	}
	_, err := claims.ApplyReanchor(store, claim.ID, resolution,
		claims.ApplyReanchorOptions{RunID: runID, At: t0})
	require.Error(t, err)
}

func TestApplyClaimInvalidations_ReadError(t *testing.T) {
	store := claims.NewClaimStore(&errBackend{})
	report := claims.ClaimInvalidationReport{
		Invalidated: []claims.ClaimInvalidation{
			{ClaimID: "c1", ProposedStatus: schemas.ClaimStatusSuspect, Symbol: "s", DocPath: "d", PreviousStatus: schemas.ClaimStatusVerified},
		},
	}
	_, err := claims.ApplyClaimInvalidations(claims.ClaimInvalidationApplyInput{
		InvalidationReport: report, Store: store, RunID: runID, At: t0,
	})
	require.Error(t, err)
}

func TestApplyClaimInvalidations_EmptyReport(t *testing.T) {
	store := newClaimStore(t)
	applyReport, err := claims.ApplyClaimInvalidations(claims.ClaimInvalidationApplyInput{
		InvalidationReport: claims.ClaimInvalidationReport{},
		Store: store, RunID: runID, At: t0,
	})
	require.NoError(t, err)
	require.Empty(t, applyReport.Applied)
	require.Empty(t, applyReport.Conflicts)
	require.Empty(t, applyReport.Rejected)
}

func TestApplyClaimInvalidations_TransitionError(t *testing.T) {
	store := newClaimStore(t)
	report := claims.ClaimInvalidationReport{
		Invalidated: []claims.ClaimInvalidation{
			{ClaimID: "nonexistent", ProposedStatus: schemas.ClaimStatusSuspect, Symbol: "s", DocPath: "d", PreviousStatus: schemas.ClaimStatusVerified},
		},
	}
	_, err := claims.ApplyClaimInvalidations(claims.ClaimInvalidationApplyInput{
		InvalidationReport: report, Store: store, RunID: runID, At: t0,
	})
	require.Error(t, err)
}

func TestIntakeClaims_EmptyTimestamp(t *testing.T) {
	store := newClaimStore(t)
	_, err := claims.IntakeClaims(claims.IntakeClaimInput{
		Concepts:    conceptsFixture(),
		DocContents: docContents,
		RunID:       runID,
		Store:       store,
		Now:         func() string { return "" },
	})
	require.Error(t, err)
}

func TestIntakeClaims_NoClaimsInEntry(t *testing.T) {
	store := newClaimStore(t)
	report, err := claims.IntakeClaims(claims.IntakeClaimInput{
		Concepts: schemas.ConceptsConfig{
			Schema: "docsync.concepts/v1",
			Docs: []schemas.ConceptsDocEntry{
				{Path: "docs/payments.md", Claims: nil},
			},
		},
		DocContents: docContents,
		RunID:       runID,
		Store:       store,
		Now:         func() string { return t0 },
	})
	require.NoError(t, err)
	require.Equal(t, 0, report.Created)
}

func TestIntakeClaims_PreExistingSkipped(t *testing.T) {
	store := newClaimStore(t)
	concepts := conceptsFixture()
	// Pre-upsert claims matching the fixture ones with all required fields
	claimA := schemas.Claim{
		ID:   "claim_a",
		Text: span1,
		Anchor: schemas.ClaimAnchor{
			File: "docs/payments.md", StartLine: 2, EndLine: 2,
			ContentHash: claims.HashSpan(span1),
		},
		Source: schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
		Status: schemas.ClaimStatusVerified,
	}
	claimB := schemas.Claim{
		ID:   "claim_b",
		Text: span2,
		Anchor: schemas.ClaimAnchor{
			File: "docs/payments.md", StartLine: 3, EndLine: 4,
			ContentHash: claims.HashSpan(span2 + "\n"),
		},
		Source:  schemas.ClaimSource{File: "src/payments/intent.ts", BlobSHA: "def5678"},
		Concept: strptr("payments"),
		Status:  schemas.ClaimStatusVerified,
	}
	mustUpsert(t, store, claimA)
	mustUpsert(t, store, claimB)

	report, err := claims.IntakeClaims(claims.IntakeClaimInput{
		Concepts:    concepts,
		DocContents: docContents,
		RunID:       runID,
		Store:       store,
		Now:         func() string { return t0 },
	})
	require.NoError(t, err)
	require.Equal(t, 0, report.Created)
	require.Equal(t, 2, report.SkippedExisting)
}

func TestDeriveClaimInvalidations_UnchangedInChangedMap(t *testing.T) {
	report := claims.DeriveClaimInvalidations(
		schemas.SymbolsDetail{
			Added:   []string{},
			Removed: []string{},
			Changed: []schemas.SymbolChangeDetail{
				{Name: "myFunc", ChangeKinds: []schemas.SymbolChangeKind{}},
			},
		},
		[]claims.SymbolBoundClaim{
			{ClaimID: "c1", Symbol: "myFunc", DocPath: "d", Status: schemas.ClaimStatusVerified},
		},
	)
	require.Len(t, report.Unchanged, 1)
	require.Equal(t, "symbol-unchanged", report.Unchanged[0].Reason)
}

func TestMergeHistory_DedupeEvents(t *testing.T) {
	existing := schemas.Claim{
		History: []schemas.ClaimHistoryEvent{
			{At: t0, RunID: runID, Event: "created"},
		},
	}
	incoming := schemas.Claim{
		History: []schemas.ClaimHistoryEvent{
			{At: t0, RunID: runID, Event: "created"},
			{At: t0, RunID: "run_2", Event: "modified"},
		},
	}
	merged := claims.MergeHistory(existing, incoming)
	require.Len(t, merged, 2)
	require.Equal(t, "modified", merged[1].Event)
}

func TestApplyReanchor_CASConflict(t *testing.T) {
	store := newClaimStore(t)
	mustUpsert(t, store, makeClaim())
	h0, err := store.HashOf("claim_1")
	require.NoError(t, err)
	concurrent := makeClaim()
	concurrent.Status = schemas.ClaimStatusSuspect
	_, err = store.Upsert(concurrent, &claims.ClaimUpsertOptions{ExpectedHash: h0})
	require.NoError(t, err)
	resolution := claims.AnchorResolution{
		Status: claims.AnchorReAnchored, StartLine: 3, EndLine: 3,
		Previous: &claims.LineRange{StartLine: 2, EndLine: 2},
	}
	result, err := claims.ApplyReanchor(store, "claim_1", resolution,
		claims.ApplyReanchorOptions{RunID: runID, At: t0, ExpectedHash: h0})
	require.NoError(t, err)
	require.False(t, result.OK)
	require.NotNil(t, result.ExpectedHash)
	require.NotNil(t, result.ActualHash)
}

func TestMarkStale_Transition(t *testing.T) {
	store := newClaimStore(t)
	mustUpsert(t, store, makeClaim())
	result, err := claims.MarkStale(store, "claim_1", claims.ClaimTransitionOptions{
		RunID: runID, Reason: "symbol removed", At: t0,
	})
	require.NoError(t, err)
	require.True(t, result.OK)
	require.Equal(t, schemas.ClaimStatusStale, result.Claim.Status)
}
