package claims_test

// Black-box port of packages/core/test/claims.test.ts and
// claim-invalidation.test.ts (P1-T10/T11 + P11-T06; ARCH §4.3):
//   - V-P1-7: content-hash match re-anchors; ambiguous edit → UNANCHORED
//     (REVIEW), never silent (invariant 13);
//   - ClaimStore: CAS-guarded upsert with append-only history merge (conflict
//     shape mirrors the store contract tests), list/get;
//   - intake happy path + typed config errors (concepts.yml is human-owned);
//   - status transitions append history, CAS-guarded;
//   - claim invalidation rules (V-P11-1) and the wiring that routes proposed
//     transitions through the real claim state machine (F1b).
//
// The TS suite used the InMemoryStateStore fake (CAS-parity with the real
// store) plus one real-StateStore smoke for listClaimIds; the fake is
// re-created here as an in-memory ClaimStoreBackend that validates through
// the same registry parser and hashes the same canonical bytes as
// *state.StateStore, and the smoke test drives the real store.

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------ golden data */

const (
	t0    = "2026-10-05T10:00:00Z"
	runID = "run_01J9ZK6X8A"
)

var (
	span1 = "Refunds require an idempotency key."
	span2 = "Payment intents are server-side."

	docV1 = strings.Join([]string{"# Payments", span1, span2, "", "See the API reference."}, "\n")
)

func makeClaim() schemas.Claim {
	return schemas.Claim{
		ID:   "claim_1",
		Text: span1,
		Anchor: schemas.ClaimAnchor{
			File:        "docs/payments.md",
			StartLine:   2,
			EndLine:     2,
			ContentHash: claims.HashSpan(span1),
		},
		Source: schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
		Status: schemas.ClaimStatusVerified,
		History: []schemas.ClaimHistoryEvent{
			{At: t0, RunID: runID, Event: "intake: hand-anchored claim created"},
		},
	}
}

func strptr(s string) *string { return &s }

/* ------------------------------------------- in-memory state-store fake */

// inMemoryClaimBackend is the CAS-parity in-memory backend: claims are
// validated through the same registry parser as the real store and hashed
// over the same canonical JSON bytes, so conflict shapes and hashes match.
type inMemoryClaimBackend struct {
	mu     sync.Mutex
	claims map[string]inMemoryClaimEntry
}

type inMemoryClaimEntry struct {
	claim schemas.Claim
	hash  string
}

func newInMemoryBackend() *inMemoryClaimBackend {
	return &inMemoryClaimBackend{claims: map[string]inMemoryClaimEntry{}}
}

func (b *inMemoryClaimBackend) ReadClaim(claimID string) (state.StateRead[*schemas.Claim], error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.claims[claimID]
	if !ok {
		return state.StateRead[*schemas.Claim]{}, nil
	}
	claim := entry.claim
	return state.StateRead[*schemas.Claim]{Content: &claim, Hash: entry.hash, Exists: true}, nil
}

func (b *inMemoryClaimBackend) WriteClaim(claimID string, claim schemas.Claim, expectedHash *string) (state.CasWriteResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	existing, exists := b.claims[claimID]
	if exists {
		if expectedHash == nil || *expectedHash != existing.hash {
			actual := existing.hash
			return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: &actual}, nil
		}
	} else if expectedHash != nil {
		return state.CasWriteResult{OK: false, ExpectedHash: expectedHash, ActualHash: nil}, nil
	}
	wire, err := json.Marshal(claim)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	parsed, err := schemas.ParseClaim(wire)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	hash, err := canonicalClaimHash(parsed)
	if err != nil {
		return state.CasWriteResult{}, err
	}
	b.claims[claimID] = inMemoryClaimEntry{claim: *parsed, hash: hash}
	return state.CasWriteResult{OK: true, Hash: hash}, nil
}

func (b *inMemoryClaimBackend) ListClaimIds() ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.claims))
	for id := range b.claims {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// canonicalClaimHash renders a claim as canonical JSON (the exact bytes CAS
// hashes and state files carry) and returns its content hash.
func canonicalClaimHash(claim *schemas.Claim) (string, error) {
	wire, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(wire))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	raw, err := docutil.CanonicalJSON(generic)
	if err != nil {
		return "", err
	}
	return docutil.ContentHash(raw), nil
}

func newClaimStore(t *testing.T) *claims.ClaimStore {
	t.Helper()
	return claims.NewClaimStore(newInMemoryBackend())
}

// mustUpsert upserts with the TS default (no expectation) and fails loudly.
func mustUpsert(t *testing.T, store *claims.ClaimStore, claim schemas.Claim) claims.ClaimUpsert {
	t.Helper()
	write, err := store.Upsert(claim, nil)
	require.NoError(t, err)
	return write
}

func claimIDs(list []schemas.Claim) []string {
	ids := make([]string, 0, len(list))
	for _, claim := range list {
		ids = append(ids, claim.ID)
	}
	return ids
}

func lastEvent(claim *schemas.Claim) schemas.ClaimHistoryEvent {
	return claim.History[len(claim.History)-1]
}

func findInvalidation(entries []claims.ClaimInvalidation, claimID string) *claims.ClaimInvalidation {
	for i := range entries {
		if entries[i].ClaimID == claimID {
			return &entries[i]
		}
	}
	return nil
}

func findUnchanged(entries []claims.ClaimUnchanged, claimID string) *claims.ClaimUnchanged {
	for i := range entries {
		if entries[i].ClaimID == claimID {
			return &entries[i]
		}
	}
	return nil
}

/* --------------------------- V-P1-7: content-hash anchoring (invariant 13) */

func TestVP1T7_ContentHashMatchReAnchorsAmbiguousEditRoutesReviewNeverSilent(t *testing.T) {
	t.Run("a moved span (unique hash match) re-anchors with a history event naming the run", func(t *testing.T) {
		claim := makeClaim()
		docMoved := strings.Join([]string{"# Payments", "Intro line.", "Second intro.", span1, ""}, "\n")
		resolution := claims.ResolveAnchor(claim.Anchor, docMoved)
		require.Equal(t, claims.AnchorReAnchored, resolution.Status)
		require.Equal(t, 4, resolution.StartLine)
		require.Equal(t, 4, resolution.EndLine)
		require.Equal(t, &claims.LineRange{StartLine: 2, EndLine: 2}, resolution.Previous)
	})

	t.Run("applyReanchor persists the new lines, keeps the hash, appends the run_id event", func(t *testing.T) {
		store := newClaimStore(t)
		claim := makeClaim()
		mustUpsert(t, store, claim)
		docMoved := strings.Join([]string{"# Payments", "Intro line.", "Second intro.", span1, ""}, "\n")
		resolution := claims.ResolveAnchor(claim.Anchor, docMoved)
		require.Equal(t, claims.AnchorReAnchored, resolution.Status)
		applied, err := claims.ApplyReanchor(store, claim.ID, resolution,
			claims.ApplyReanchorOptions{RunID: runID, At: t0})
		require.NoError(t, err)
		require.True(t, applied.OK)
		require.NotNil(t, applied.Claim)
		require.Equal(t, 4, applied.Claim.Anchor.StartLine)
		require.Equal(t, 4, applied.Claim.Anchor.EndLine)
		require.Equal(t, claims.HashSpan(span1), applied.Claim.Anchor.ContentHash)
		require.Equal(t, schemas.ClaimStatusVerified, applied.Claim.Status)
		// History is append-only: the original intake event survives untouched.
		require.Equal(t, claim.History[0], applied.Claim.History[0])
		event := lastEvent(applied.Claim)
		require.Equal(t, "re-anchored (content-hash match)", event.Event)
		require.Equal(t, runID, event.RunID)
	})

	t.Run("an edited span (hash matches nowhere) is unanchored, never re-anchored", func(t *testing.T) {
		claim := makeClaim()
		docEdited := strings.Join([]string{"# Payments", "Refunds require an idempotency token.", span2}, "\n")
		resolution := claims.ResolveAnchor(claim.Anchor, docEdited)
		require.Equal(t, claims.AnchorResolution{
			Status: claims.AnchorUnanchored,
			Reason: "content hash matches nowhere — doc edited",
		}, resolution)
	})

	t.Run("a duplicated span is ambiguous → unanchored, NEVER silently re-anchored (invariant 13)", func(t *testing.T) {
		claim := makeClaim()
		docDuplicated := strings.Join([]string{"# Payments", span1, "filler", span1, ""}, "\n")
		resolution := claims.ResolveAnchor(claim.Anchor, docDuplicated)
		require.Equal(t, claims.AnchorUnanchored, resolution.Status)
		require.Contains(t, resolution.Reason, "ambiguous — 2 identical spans")
	})

	t.Run("multi-line spans re-anchor as a unit; out-of-bounds recorded lines are unanchored", func(t *testing.T) {
		multiline := makeClaim()
		multiline.Anchor = schemas.ClaimAnchor{
			File:        "docs/payments.md",
			StartLine:   2,
			EndLine:     3,
			ContentHash: claims.HashSpan(span1 + "\n" + span2),
		}
		unchanged := claims.ResolveAnchor(multiline.Anchor, docV1)
		require.Equal(t, claims.AnchorUnchanged, unchanged.Status)
		require.Equal(t, 2, unchanged.StartLine)
		require.Equal(t, 3, unchanged.EndLine)

		docShifted := strings.Join([]string{"# Payments", "filler", span1, span2, "tail"}, "\n")
		shifted := claims.ResolveAnchor(multiline.Anchor, docShifted)
		require.Equal(t, claims.AnchorReAnchored, shifted.Status)
		require.Equal(t, 3, shifted.StartLine)
		require.Equal(t, 4, shifted.EndLine)

		oob := claims.ResolveAnchor(multiline.Anchor, "only one line\n")
		require.Equal(t, claims.AnchorUnanchored, oob.Status)
		require.Contains(t, oob.Reason, "outside the document bounds")
	})

	t.Run("markUnanchored routes the failed resolution into status + history (REVIEW)", func(t *testing.T) {
		store := newClaimStore(t)
		claim := makeClaim()
		mustUpsert(t, store, claim)
		docEdited := strings.Join([]string{"# Payments", "Refunds require an idempotency token."}, "\n")
		resolution := claims.ResolveAnchor(claim.Anchor, docEdited)
		require.Equal(t, claims.AnchorUnanchored, resolution.Status)
		result, err := claims.MarkUnanchored(store, claim.ID, claims.ClaimTransitionOptions{
			RunID:  runID,
			Reason: resolution.Reason,
			At:     t0,
		})
		require.NoError(t, err)
		require.True(t, result.OK)
		require.NotNil(t, result.Claim)
		require.Equal(t, schemas.ClaimStatusUnanchored, result.Claim.Status)
		require.Contains(t, lastEvent(result.Claim).Event, "unanchored: content hash matches nowhere")
		require.Equal(t, runID, lastEvent(result.Claim).RunID)
	})

	t.Run("applyReanchor refuses non-re-anchored resolutions (no silent anchor moves)", func(t *testing.T) {
		store := newClaimStore(t)
		mustUpsert(t, store, makeClaim())
		bogus := claims.AnchorResolution{Status: claims.AnchorUnchanged, StartLine: 9, EndLine: 9}
		_, err := claims.ApplyReanchor(store, "claim_1", bogus,
			claims.ApplyReanchorOptions{RunID: runID, At: t0})
		require.Error(t, err)
		require.Contains(t, err.Error(), "re-anchored")
	})
}

/* ---------------------- ClaimStore: CAS-guarded upsert + history merge */

func TestClaimStore_CASGuardedUpsertWithAppendOnlyHistoryMerge(t *testing.T) {
	t.Run("upsert conflicts on a stale expectedHash and the loser re-runs (store contract style)", func(t *testing.T) {
		store := newClaimStore(t)
		created := mustUpsert(t, store, makeClaim())
		require.True(t, created.OK)
		require.True(t, created.Created)
		h0 := created.Hash

		// Concurrent writer B wins with the same expected hash.
		writerBClaim := makeClaim()
		writerBClaim.Status = schemas.ClaimStatusSuspect
		writerB, err := store.Upsert(writerBClaim, &claims.ClaimUpsertOptions{ExpectedHash: &h0})
		require.NoError(t, err)
		require.True(t, writerB.OK)
		require.False(t, writerB.Created)

		// Stale writer A loses with the conflict shape; winner's bytes survive.
		staleAClaim := makeClaim()
		staleAClaim.Status = schemas.ClaimStatusStale
		staleA, err := store.Upsert(staleAClaim, &claims.ClaimUpsertOptions{ExpectedHash: &h0})
		require.NoError(t, err)
		require.Equal(t, claims.ClaimUpsert{
			OK:           false,
			ExpectedHash: &h0,
			ActualHash:   &writerB.Hash,
		}, staleA)
		after, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, after)
		require.Equal(t, schemas.ClaimStatusSuspect, after.Status)

		// The loser re-runs without an expectation and lands its update (merged).
		reRunClaim := makeClaim()
		reRunClaim.Status = schemas.ClaimStatusStale
		reRun, err := store.Upsert(reRunClaim, nil)
		require.NoError(t, err)
		require.True(t, reRun.OK)
		require.False(t, reRun.Created)
		final, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, final)
		require.Equal(t, schemas.ClaimStatusStale, final.Status)
		// identical events dedupe (idempotent)
		require.Len(t, final.History, 1)
	})

	t.Run("upsert merges distinct history events append-only, never rewriting them", func(t *testing.T) {
		store := newClaimStore(t)
		base := makeClaim()
		mustUpsert(t, store, base)
		incoming := makeClaim()
		incoming.History = []schemas.ClaimHistoryEvent{
			{At: t0, RunID: "run_OTHER", Event: "support lost: refund flow changed"},
		}
		write, err := store.Upsert(incoming, nil)
		require.NoError(t, err)
		require.True(t, write.OK)
		merged, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, merged)
		require.Equal(t, []schemas.ClaimHistoryEvent{
			base.History[0],
			{At: t0, RunID: "run_OTHER", Event: "support lost: refund flow changed"},
		}, merged.History)
	})

	t.Run("an explicit expectedHash on an absent claim conflicts with actualHash null", func(t *testing.T) {
		store := newClaimStore(t)
		result, err := store.Upsert(makeClaim(), &claims.ClaimUpsertOptions{ExpectedHash: strptr("sha256:deadbeef")})
		require.NoError(t, err)
		require.Equal(t, claims.ClaimUpsert{
			OK:           false,
			ExpectedHash: strptr("sha256:deadbeef"),
			ActualHash:   nil,
		}, result)
	})

	t.Run("get returns nil for absent claims; list is sorted by id", func(t *testing.T) {
		store := newClaimStore(t)
		absent, err := store.Get("nope")
		require.NoError(t, err)
		require.Nil(t, absent)
		list, err := store.List()
		require.NoError(t, err)
		require.Empty(t, list)

		claimB := makeClaim()
		claimB.ID = "claim_b"
		mustUpsert(t, store, claimB)
		claimA := makeClaim()
		claimA.ID = "claim_a"
		mustUpsert(t, store, claimA)
		list, err = store.List()
		require.NoError(t, err)
		require.Equal(t, []string{"claim_a", "claim_b"}, claimIDs(list))
	})

	t.Run("loseSupport transitions to suspect with a history event (CAS-guarded)", func(t *testing.T) {
		store := newClaimStore(t)
		mustUpsert(t, store, makeClaim())
		result, err := claims.LoseSupport(store, "claim_1", claims.ClaimTransitionOptions{
			RunID:  runID,
			Reason: "mapped source src/payments/refund.ts changed",
			At:     t0,
		})
		require.NoError(t, err)
		require.True(t, result.OK)
		require.NotNil(t, result.Claim)
		require.Equal(t, schemas.ClaimStatusSuspect, result.Claim.Status)
		require.Equal(t,
			"support lost: mapped source src/payments/refund.ts changed",
			lastEvent(result.Claim).Event)
	})
}

/* --------------------- hand-anchored/manifest-declared intake (P1-T11) */

func conceptsFixture() schemas.ConceptsConfig {
	return schemas.ConceptsConfig{
		Schema: "docsync.concepts/v1",
		Docs: []schemas.ConceptsDocEntry{
			{
				Path: "docs/payments.md",
				Claims: []schemas.ConceptsClaimDeclaration{
					{
						ID:   "claim_a",
						Text: span1,
						Anchor: schemas.ConceptsClaimAnchor{
							File:      "docs/payments.md",
							StartLine: 2,
							EndLine:   2,
						},
						Source: schemas.ClaimSource{File: "src/payments/refund.ts", BlobSHA: "abc1234"},
					},
					{
						ID:   "claim_b",
						Text: span2,
						Anchor: schemas.ConceptsClaimAnchor{
							File:        "docs/payments.md",
							StartLine:   3,
							EndLine:     4,
							ContentHash: strptr(claims.HashSpan(span2 + "\n")),
						},
						Source:  schemas.ClaimSource{File: "src/payments/intent.ts", BlobSHA: "def5678"},
						Concept: strptr("payments"),
					},
				},
			},
		},
	}
}

func docContents(docPath string) *string {
	if docPath == "docs/payments.md" {
		return &docV1
	}
	return nil
}

func intake(t *testing.T, store *claims.ClaimStore, concepts schemas.ConceptsConfig) claims.IntakeReport {
	t.Helper()
	report, err := claims.IntakeClaims(claims.IntakeClaimInput{
		Concepts:    concepts,
		DocContents: docContents,
		RunID:       runID,
		Store:       store,
		Now:         func() string { return t0 },
	})
	require.NoError(t, err)
	return report
}

func TestIntake_HandAnchoredManifestDeclaredClaims(t *testing.T) {
	t.Run("creates claims, computing content_hash from the span when absent", func(t *testing.T) {
		store := newClaimStore(t)
		report := intake(t, store, conceptsFixture())
		require.Equal(t, claims.IntakeReport{
			Created:         2,
			SkippedExisting: 0,
			Errors:          []claims.IntakeError{},
		}, report)

		a, err := store.Get("claim_a")
		require.NoError(t, err)
		require.NotNil(t, a)
		require.Equal(t, schemas.ClaimAnchor{
			File:        "docs/payments.md",
			StartLine:   2,
			EndLine:     2,
			ContentHash: claims.HashSpan(span1),
		}, a.Anchor)
		require.Equal(t, schemas.ClaimStatusVerified, a.Status)
		require.Equal(t, runID, a.History[0].RunID)
		require.Equal(t, t0, a.History[0].At)
		require.Contains(t, a.History[0].Event, "concepts.yml")

		b, err := store.Get("claim_b")
		require.NoError(t, err)
		require.NotNil(t, b)
		// declared hash trusted
		require.Equal(t, claims.HashSpan(span2+"\n"), b.Anchor.ContentHash)
		require.NotNil(t, b.Concept)
		require.Equal(t, "payments", *b.Concept)
	})

	t.Run("is idempotent: a second intake skips existing claims", func(t *testing.T) {
		store := newClaimStore(t)
		first := intake(t, store, conceptsFixture())
		require.Equal(t, 2, first.Created)
		second := intake(t, store, conceptsFixture())
		require.Equal(t, claims.IntakeReport{
			Created:         0,
			SkippedExisting: 2,
			Errors:          []claims.IntakeError{},
		}, second)
	})

	t.Run("fails closed with typed config errors (missing doc, out-of-range, empty span, mismatch, dup id)", func(t *testing.T) {
		conceptsConfig := schemas.ConceptsConfig{
			Schema: "docsync.concepts/v1",
			Docs: []schemas.ConceptsDocEntry{
				{
					Path: "docs/ghost.md",
					Claims: []schemas.ConceptsClaimDeclaration{
						{
							ID:     "claim_missing_doc",
							Text:   "x",
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/ghost.md", StartLine: 1, EndLine: 1},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "aaa"},
						},
					},
				},
				{
					Path: "docs/payments.md",
					Claims: []schemas.ConceptsClaimDeclaration{
						{
							ID:     "claim_oob",
							Text:   "x",
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 99, EndLine: 100},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "bbb"},
						},
						{
							ID:     "claim_blank",
							Text:   "x",
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 4, EndLine: 4},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "ccc"},
						},
						{
							ID:     "claim_mismatch",
							Text:   span1,
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/other.md", StartLine: 2, EndLine: 2},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "ddd"},
						},
						{
							ID:     "claim_dup",
							Text:   span1,
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 2, EndLine: 2},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "eee"},
						},
						{
							ID:     "claim_dup",
							Text:   span1,
							Anchor: schemas.ConceptsClaimAnchor{File: "docs/payments.md", StartLine: 2, EndLine: 2},
							Source: schemas.ClaimSource{File: "src/x.ts", BlobSHA: "fff"},
						},
					},
				},
			},
		}
		store := newClaimStore(t)
		report := intake(t, store, conceptsConfig)
		// the FIRST claim_dup declaration is valid (first wins)
		require.Equal(t, 1, report.Created)
		kinds := make([]string, 0, len(report.Errors))
		for _, entry := range report.Errors {
			kinds = append(kinds, string(entry.Kind))
		}
		sort.Strings(kinds)
		require.Equal(t, []string{
			"anchor_file_mismatch",
			"doc_content_unavailable",
			"duplicate_claim_id",
			"empty_span",
			"lines_out_of_range",
		}, kinds)
		list, err := store.List()
		require.NoError(t, err)
		require.Equal(t, []string{"claim_dup"}, claimIDs(list))
	})
}

/* -------------------- ClaimStore over the real StateStore (listClaimIds smoke) */

func TestClaimStoreOverRealStateStore_ListClaimIdsSmoke(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".docsync", "state")
	backend, err := state.NewStateStore(state.StateStoreOptions{StateDir: stateDir})
	require.NoError(t, err)

	ids, err := backend.ListClaimIds()
	require.NoError(t, err)
	require.Empty(t, ids)

	store := claims.NewClaimStore(backend)
	claimB := makeClaim()
	claimB.ID = "claim_b"
	mustUpsert(t, store, claimB)
	claimA := makeClaim()
	claimA.ID = "claim_a"
	mustUpsert(t, store, claimA)

	ids, err = backend.ListClaimIds()
	require.NoError(t, err)
	require.Equal(t, []string{"claim_a", "claim_b"}, ids)
	list, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"claim_a", "claim_b"}, claimIDs(list))
	got, err := store.Get("claim_a")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "claim_a", got.ID)
	require.Equal(t, schemas.ClaimStatusVerified, got.Status)
}

/* ------------------------------ claim invalidation rules (P11-T06; V-P11-1) */

var symbolsDetail = schemas.SymbolsDetail{
	Added:   []string{"newHelper"},
	Removed: []string{"legacyParser"},
	Changed: []schemas.SymbolChangeDetail{
		{Name: "idempotencyKey", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}},
		{Name: "createRefund", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeSignature}},
	},
}

func bound(claimID, symbol, docPath string) claims.SymbolBoundClaim {
	return claims.SymbolBoundClaim{
		ClaimID: claimID,
		Symbol:  symbol,
		DocPath: docPath,
		Status:  schemas.ClaimStatusVerified,
	}
}

func TestClaimInvalidationRules_VP11(t *testing.T) {
	t.Run("V-P11-1: a type-level change on idempotencyKey proposes verified→suspect with symbol-level evidence; untouched claims are explicitly unchanged", func(t *testing.T) {
		report := claims.DeriveClaimInvalidations(symbolsDetail, []claims.SymbolBoundClaim{
			bound("c1", "idempotencyKey", "docs/payments.md"),
			bound("c2", "createRefund", "docs/payments.md"),
			bound("c3", "untouchedFn", "docs/other.md"),
		})
		require.Len(t, report.Invalidated, 2)

		c1 := findInvalidation(report.Invalidated, "c1")
		require.NotNil(t, c1)
		require.Equal(t, schemas.ClaimStatusVerified, c1.PreviousStatus)
		require.Equal(t, schemas.ClaimStatusSuspect, c1.ProposedStatus)
		require.Equal(t, []string{"type"}, c1.Evidence[0].ChangeKinds)

		c2 := findInvalidation(report.Invalidated, "c2")
		require.NotNil(t, c2)
		require.Equal(t, schemas.ClaimStatusSuspect, c2.ProposedStatus)
		require.Equal(t, "signature", c2.Reason)

		c3 := findUnchanged(report.Unchanged, "c3")
		require.NotNil(t, c3)
		require.Equal(t, "symbol-not-present", c3.Reason)

		// the rule table is exported versioned data
		require.Equal(t, schemas.ClaimStatusSuspect, claims.ClaimInvalidationRules["type"])
		require.Equal(t, schemas.ClaimStatusStale, claims.ClaimInvalidationRules["removed"])
	})

	t.Run("removed/added symbols propose stale; empty evidence proposes nothing (no fabrication)", func(t *testing.T) {
		report := claims.DeriveClaimInvalidations(symbolsDetail, []claims.SymbolBoundClaim{
			bound("c4", "legacyParser", "docs/x.md"),
			bound("c5", "newHelper", "docs/y.md"),
		})
		require.Len(t, report.Invalidated, 2)
		for _, entry := range report.Invalidated {
			require.Equal(t, schemas.ClaimStatusStale, entry.ProposedStatus)
		}

		empty := claims.DeriveClaimInvalidations(
			schemas.SymbolsDetail{Added: []string{}, Removed: []string{}, Changed: []schemas.SymbolChangeDetail{}},
			[]claims.SymbolBoundClaim{bound("c1", "idempotencyKey", "d")})
		require.Empty(t, empty.Invalidated)
		require.Len(t, empty.Unchanged, 1)
	})

	t.Run("visibility changes propose stale; multi-kind evidence takes the FIRST rule match deterministically", func(t *testing.T) {
		report := claims.DeriveClaimInvalidations(
			schemas.SymbolsDetail{
				Added:   []string{},
				Removed: []string{},
				Changed: []schemas.SymbolChangeDetail{
					{Name: "exportedFn", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeVisibility}},
					{Name: "bothKinds", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeVisibility, schemas.SymbolChangeType}},
				},
			},
			[]claims.SymbolBoundClaim{
				bound("c6", "exportedFn", "d"),
				bound("c7", "bothKinds", "d"),
			})
		c6 := findInvalidation(report.Invalidated, "c6")
		require.NotNil(t, c6)
		require.Equal(t, schemas.ClaimStatusStale, c6.ProposedStatus)
		// type precedes visibility in the rule-table iteration order of the evidence
		c7 := findInvalidation(report.Invalidated, "c7")
		require.NotNil(t, c7)
		require.Equal(t, "type", c7.Reason)
	})
}

/* ------------- applyClaimInvalidations through the real claim state machine (F1b) */

func wiredStore(t *testing.T, stored []schemas.Claim) *claims.ClaimStore {
	t.Helper()
	store := newClaimStore(t)
	for _, claim := range stored {
		mustUpsert(t, store, claim)
	}
	return store
}

func applyInvalidations(t *testing.T, store *claims.ClaimStore, report claims.ClaimInvalidationReport, expectedHashByClaim map[string]string) claims.ClaimInvalidationApplyReport {
	t.Helper()
	applyReport, err := claims.ApplyClaimInvalidations(claims.ClaimInvalidationApplyInput{
		InvalidationReport:  report,
		Store:               store,
		RunID:               runID,
		At:                  t0,
		ExpectedHashByClaim: expectedHashByClaim,
	})
	require.NoError(t, err)
	return applyReport
}

func TestApplyClaimInvalidations_ThroughTheRealClaimStateMachine(t *testing.T) {
	t.Run("V-P11-1 wiring: the invalidation-driven transition flows through the real claim state machine — the bound claim flips verified→suspect in the store, an invalid proposed transition surfaces as a reported rejection", func(t *testing.T) {
		claim1 := makeClaim()
		claim2 := makeClaim()
		claim2.ID = "claim_2"
		claim2.Text = "Untouched claim."
		store := wiredStore(t, []schemas.Claim{claim1, claim2})

		report := claims.DeriveClaimInvalidations(
			schemas.SymbolsDetail{
				Added:   []string{},
				Removed: []string{},
				Changed: []schemas.SymbolChangeDetail{
					{Name: "idempotencyKey", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}},
				},
			},
			[]claims.SymbolBoundClaim{
				bound("claim_1", "idempotencyKey", "docs/payments.md"),
				bound("claim_2", "untouchedFn", "docs/payments.md"),
			})
		applyReport := applyInvalidations(t, store, report, nil)

		// The machine accepted exactly the bound claim's transition.
		require.Len(t, applyReport.Applied, 1)
		require.Empty(t, applyReport.Rejected)
		require.Empty(t, applyReport.Conflicts)
		applied := applyReport.Applied[0]
		require.Equal(t, "claim_1", applied.ClaimID)
		require.Equal(t, "idempotencyKey", applied.Symbol)
		require.Equal(t, "docs/payments.md", applied.DocPath)
		require.Equal(t, schemas.ClaimStatusVerified, applied.From)
		require.Equal(t, schemas.ClaimStatusSuspect, applied.To)
		require.Equal(t, "type", applied.Reason)
		require.Equal(t, "support lost: symbol evidence: idempotencyKey (type) [type]", applied.Event)
		// the CAS hash is a bare hex sha256 digest (state/hash.ts digest('hex') — no prefix)
		require.Regexp(t, `^[0-9a-f]{64}$`, applied.Hash)

		// The STORE flipped: the transition happened through the machine, not beside it.
		stored, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, stored)
		require.Equal(t, schemas.ClaimStatusSuspect, stored.Status)
		require.Equal(t, "support lost: symbol evidence: idempotencyKey (type) [type]", lastEvent(stored).Event)
		require.Equal(t, runID, lastEvent(stored).RunID)
		require.Equal(t, t0, lastEvent(stored).At)
		require.Equal(t, schemas.ClaimHistoryEvent{
			At: t0, RunID: runID, Event: "intake: hand-anchored claim created",
		}, stored.History[0])

		// An unchanged claim is untouched in the store.
		untouched, err := store.Get("claim_2")
		require.NoError(t, err)
		require.NotNil(t, untouched)
		require.Equal(t, schemas.ClaimStatusVerified, untouched.Status)
		require.Len(t, untouched.History, 1)

		// Deliberately-invalid proposed transition: 'verified' has NO
		// invalidation-owned machine function (re-verification belongs to the
		// verifier) → typed rejection, the status is NOT written, and no
		// history event appears.
		bogus := claims.ClaimInvalidation{
			ClaimID:        "claim_1",
			Symbol:         "idempotencyKey",
			DocPath:        "docs/payments.md",
			PreviousStatus: schemas.ClaimStatusSuspect,
			ProposedStatus: schemas.ClaimStatusVerified,
			Evidence:       []claims.SymbolChangeEvidence{{Name: "idempotencyKey", ChangeKinds: []string{"type"}}},
			Reason:         "type",
		}
		refused := applyInvalidations(t, store, claims.ClaimInvalidationReport{
			Invalidated: []claims.ClaimInvalidation{bogus},
			Unchanged:   []claims.ClaimUnchanged{},
		}, nil)
		require.Empty(t, refused.Applied)
		require.Empty(t, refused.Conflicts)
		require.Len(t, refused.Rejected, 1)
		require.Equal(t, "claim_1", refused.Rejected[0].ClaimID)
		require.Equal(t, schemas.ClaimStatusSuspect, refused.Rejected[0].From)
		require.Equal(t, schemas.ClaimStatusVerified, refused.Rejected[0].To)
		require.Equal(t, "no-transition-function", refused.Rejected[0].Reason)
		require.Contains(t, refused.Rejected[0].Detail, "'verified'")
		afterRefusal, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, afterRefusal)
		require.Equal(t, schemas.ClaimStatusSuspect, afterRefusal.Status)
		require.Len(t, afterRefusal.History, 2)
	})

	t.Run("a removed symbol routes through markStale (stale: … history event, status stale in the store)", func(t *testing.T) {
		claim3 := makeClaim()
		claim3.ID = "claim_3"
		claim3.Text = "Legacy parser note."
		store := wiredStore(t, []schemas.Claim{claim3})

		report := claims.DeriveClaimInvalidations(
			schemas.SymbolsDetail{Added: []string{}, Removed: []string{"legacyParser"}, Changed: []schemas.SymbolChangeDetail{}},
			[]claims.SymbolBoundClaim{bound("claim_3", "legacyParser", "docs/parser.md")})
		applyReport := applyInvalidations(t, store, report, nil)
		require.Len(t, applyReport.Applied, 1)
		require.Equal(t, schemas.ClaimStatusStale, applyReport.Applied[0].To)
		require.Equal(t, "removed", applyReport.Applied[0].Reason)

		stored, err := store.Get("claim_3")
		require.NoError(t, err)
		require.NotNil(t, stored)
		require.Equal(t, schemas.ClaimStatusStale, stored.Status)
		require.Equal(t, "stale: symbol evidence: legacyParser (removed) [removed]", lastEvent(stored).Event)
	})

	t.Run("a stale expectedHash surfaces the machine CAS-conflict shape; the store is unchanged (R15)", func(t *testing.T) {
		store := wiredStore(t, []schemas.Claim{makeClaim()})
		stored, err := store.Get("claim_1")
		require.NoError(t, err)
		require.NotNil(t, stored)

		report := claims.DeriveClaimInvalidations(
			schemas.SymbolsDetail{
				Added:   []string{},
				Removed: []string{},
				Changed: []schemas.SymbolChangeDetail{
					{Name: "idempotencyKey", ChangeKinds: []schemas.SymbolChangeKind{schemas.SymbolChangeType}},
				},
			},
			[]claims.SymbolBoundClaim{bound("claim_1", "idempotencyKey", "docs/payments.md")})
		applyReport := applyInvalidations(t, store, report, map[string]string{"claim_1": "sha256:stalehash"})
		require.Empty(t, applyReport.Applied)
		require.Empty(t, applyReport.Rejected)

		actualHash, err := store.HashOf("claim_1")
		require.NoError(t, err)
		require.NotNil(t, actualHash)
		require.Equal(t, []claims.ConflictedClaimInvalidation{
			{
				ClaimID:      "claim_1",
				From:         schemas.ClaimStatusVerified,
				To:           schemas.ClaimStatusSuspect,
				ExpectedHash: strptr("sha256:stalehash"),
				ActualHash:   actualHash,
			},
		}, applyReport.Conflicts)
		require.Equal(t, schemas.ClaimStatusVerified, stored.Status)
	})

	t.Run("the dispatch table is exported versioned data naming the machine functions", func(t *testing.T) {
		require.Equal(t, map[string]string{"suspect": "loseSupport", "stale": "markStale"},
			claims.ClaimInvalidationTransitions)
	})
}
