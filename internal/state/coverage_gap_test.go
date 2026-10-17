package state_test

// Coverage-gap tests for internal/state: the last fail-closed path-encoding
// edges (NUL, empty decode, post-decode charset, leading ./), the upsert /
// transition revalidation floors, the CAS expected-absent conflict outcome,
// and the stem list's non-.json filter.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_STATE_PathEncodingRefusalEdges(t *testing.T) {
	// DecodeStateSegment: empty input.
	_, err := state.DecodeStateSegment("")
	require.Error(t, err)
	// DecodeStateSegment: bytes outside the safe charset refuse post-decode.
	_, err = state.DecodeStateSegment("a^b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside [A-Za-z0-9._-]")

	// NUL bytes refuse.
	_, err = state.EncodeStateDocPath("docs\x00x.md")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NUL")

	// DecodeStateDocPath empty input.
	_, err = state.DecodeStateDocPath("")
	require.Error(t, err)

	// Leading "./" segments are stripped and the path round-trips.
	encoded, err := state.EncodeStateDocPath("./docs/a.md")
	require.NoError(t, err)
	decoded, err := state.DecodeStateDocPath(encoded)
	require.NoError(t, err)
	assert.Equal(t, "docs/a.md", decoded)

	// A literal underscore escapes injectively through a full round trip.
	encoded, err = state.EncodeStateDocPath("my_docs/user_file.md")
	require.NoError(t, err)
	decoded, err = state.DecodeStateDocPath(encoded)
	require.NoError(t, err)
	assert.Equal(t, "my_docs/user_file.md", decoded)

	// Run-key repo with an unsafe segment refuses (no '#' allowed).
	_, err = state.RunRecordFilename(schemas.RunKey{
		Repo: "acme/corp#main", BaseSHA: "aaaa", HeadSHA: "bbbb",
		ManifestVersion: 1, PolicyVersion: 1,
	})
	require.Error(t, err)
}

func TestCOV_STATE_UpsertRunRecordInvalidRecordRefused(t *testing.T) {
	store, _ := newStore(t, nil)
	bad := runRecordExample
	bad.RunID = ""
	_, err := store.UpsertRunRecord(runKey, bad)
	require.Error(t, err)
	assert.Equal(t, state.CodeStateSchema, docutil.CodeOf(err))
}

func TestCOV_STATE_AppendTransitionNeedsCurrentHash(t *testing.T) {
	// Appends are CAS updates: appending WITHOUT the current hash is a
	// conflict (never an overwrite); with the hash it lands, in order.
	store, _ := newStore(t, nil)
	_, err := store.WriteDocState(docStateExample.Path, docStateExample, nil)
	require.NoError(t, err)

	// No expected hash + an existing file → CAS conflict (OK:false).
	conflict, err := store.AppendTransition(docStateExample.Path, schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "r12", Reason: "pinned",
	}, nil)
	require.NoError(t, err)
	require.False(t, conflict.OK)
	require.NotNil(t, conflict.ActualHash)

	read, err := store.ReadDocState(docStateExample.Path)
	require.NoError(t, err)
	require.Len(t, read.Content.Transitions, 1)

	// With the current hash the append lands.
	result, err := store.AppendTransition(docStateExample.Path, schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "r12", Reason: "pinned",
	}, &read.Hash)
	require.NoError(t, err)
	require.True(t, result.OK)

	read, err = store.ReadDocState(docStateExample.Path)
	require.NoError(t, err)
	require.Len(t, read.Content.Transitions, 2)
	assert.Equal(t, "r12", read.Content.Transitions[1].RunID)
}

func TestCOV_STATE_ListClaimIdsSkipsForeignStems(t *testing.T) {
	store, stateDir := newStore(t, nil)
	claimsDir := filepath.Join(stateDir, "claims")
	require.NoError(t, os.MkdirAll(claimsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claimsDir, "stray.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(claimsDir, "clm-01.json"), []byte("{}"), 0o644))

	ids, err := store.ListClaimIds()
	require.NoError(t, err)
	require.Equal(t, []string{"clm-01"}, ids)
}

func TestCOV_STATE_CasWriteExpectedWhenAbsent(t *testing.T) {
	stateDir := makeStateDir(t)
	target := filepath.Join(stateDir, "absent.json")
	expected := "deadbeef"
	result, err := state.CasWriteFileDetailed(target, `{"x":1}`, &expected)
	require.NoError(t, err)
	assert.False(t, result.OK)
	assert.Equal(t, &expected, result.ExpectedHash)
	assert.Nil(t, result.ActualHash)
	_, err = os.Stat(target)
	require.Error(t, err)
}


func TestCOV_STATE_UpsertAndAppendBadIdentifiers(t *testing.T) {
	store, _ := newStore(t, nil)

	// Upsert with an unsafe run key refuses at the routing step.
	_, err := store.UpsertRunRecord(schemas.RunKey{
		Repo: "acme/corp", BaseSHA: "not#filename#safe", HeadSHA: "bbbb",
		ManifestVersion: 1, PolicyVersion: 1,
	}, runRecordExample)
	require.Error(t, err)

	// AppendTransition with a traversal doc path refuses at routing.
	_, err = store.AppendTransition("../escape.md", schemas.DocStateTransition{
		State: schemas.DocStateClear, At: "2026-10-05T10:00:00Z", RunID: "r1",
	}, nil)
	require.Error(t, err)

	// WriteMergeLedger whose key matches the record but is not encodable.
	ledger := runRecordExample.Key
	_ = ledger
	ml := schemas.MergeLedgerExample()
	ml.SourceKey = "../evil"
	_, err = store.WriteMergeLedger(ml.SourceKey, ml, nil)
	require.Error(t, err)
}
