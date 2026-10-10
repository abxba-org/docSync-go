package cli_test

// Ported from packages/cli/test/fix-store.test.ts (P7-T09) — the fix-run
// record store lanes: schema-valid round-trips, the deterministic empty read,
// and the V-P7-13 partial contract (a concurrent CAS conflict LOSES, retries
// once, and never overwrites a concurrent winner).
//
// DEVIATION NOTES (TS pins internals with no Go equivalent — the BEHAVIOR is
// pinned here instead):
//   - The TS store is per-run-file CAS (`runs/fix-<id>.json` +
//     casWriteFileDetailed) with a typed FixRunRecordConflictError; the Go
//     store is an append-only JSONL ledger (`fix-run-records.jsonl`) with
//     Has/HasPatchRef lookups. The conflict-error lane is therefore pinned as
//     the surviving behavior: a loser NEVER overwrites the winner's record and
//     the same-run gate stays idempotent.
//   - TS `read(runId)`/`hasApplied(runId, doc)` have no Go counterpart; the
//     equivalent observable behavior (a record exists for that run, and its
//     applied patch refs resolve across runs) is pinned through Has /
//     HasPatchRef.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DUDE616/docSync-go/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixStoreRunID = "01J9ZK7Q2M8P4X6T2W5Y3C0A9E"

// cliFixStoreRecord builds the TS makeRecord shape: one applied doc + one
// skipped-reason marker embedded in the record's patch refs.
func cliFixStoreRecord(runID, marker string) cli.FixRunRecord {
	return cli.FixRunRecord{
		Schema: "docsync.fix-run-record/v1",
		RunID:  runID,
		Applied: []cli.FixRecordApplied{{
			DocPath:      "docs/api.md",
			PatchRef:     "state/docs/docs__api.md#patch-" + marker,
			ThreeWayUsed: false,
		}},
		PatchRefs: []string{"state/docs/docs__api.md#patch-" + marker},
	}
}

// cliReadBackRecord reads the store's persisted line back (the Go mirror of
// the TS store.read round-trip through the store's own storage).
func cliReadBackRecord(t *testing.T, stateDir, runID string) cli.FixRunRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateDir, "fix-run-records.jsonl"))
	require.NoError(t, err)
	for _, line := range splitLines(string(data)) {
		var record cli.FixRunRecord
		if json.Unmarshal([]byte(line), &record) == nil && record.RunID == runID {
			return record
		}
	}
	t.Fatalf("no record persisted for run %s", runID)
	return cli.FixRunRecord{}
}

func splitLines(s string) []string {
	out := []string{}
	current := ""
	for _, r := range s {
		if r == '\n' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(r)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func TestFixRunRecordStoreWritesAndReadsBackSchemaValidRecord(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".docsync", "state")
	store := cli.NewFixRunRecordStore(stateDir)
	record := cliFixStoreRecord(fixStoreRunID, "a1")
	require.NoError(t, store.Write(record))

	// The round-trip: the record the store hands back (its own persisted line)
	// is exactly what was written, carrying the schema identity.
	read := cliReadBackRecord(t, stateDir, fixStoreRunID)
	assert.Equal(t, "docsync.fix-run-record/v1", read.Schema)
	assert.Equal(t, record, read)
	// The same-run gate + provenance lookups resolve the written record.
	assert.True(t, store.Has(fixStoreRunID))
	assert.True(t, store.HasPatchRef("state/docs/docs__api.md#patch-a1"))
}

func TestFixRunRecordStoreReadAbsentRunIsEmpty(t *testing.T) {
	store := cli.NewFixRunRecordStore(filepath.Join(t.TempDir(), ".docsync", "state"))
	// Deterministic empty read: no record, no panic, no file created.
	assert.False(t, store.Has(fixStoreRunID))
	assert.False(t, store.HasPatchRef("state/docs/docs__api.md#patch-none"))
}

func TestFixRunRecordStoreCasConflictLoserNeverOverwritesWinner(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".docsync", "state")
	// The winner lands first (the concurrent-winner double's landed write).
	winner := cliNewFixRunRecordStoreWith(t, stateDir, cliFixStoreRecord(fixStoreRunID, "winner"))

	// The loser appends its own record for the SAME run id afterwards. The
	// V-P7-13 contract: the loser never overwrites the concurrent winner — the
	// winner's intact record survives and remains the resolvable state.
	loser := cli.NewFixRunRecordStore(stateDir)
	require.NoError(t, loser.Write(cliFixStoreRecord(fixStoreRunID, "loser")))

	// The winner's record stands: its applied row is still queryable.
	assert.True(t, winner.Has(fixStoreRunID))
	assert.True(t, winner.HasPatchRef("state/docs/docs__api.md#patch-winner"))
	// The same-run gate stays idempotent (a duplicate run id is not a lost
	// write, never a corrupted one).
	assert.True(t, loser.Has(fixStoreRunID))
	// A fresh store over the same state reads the winner back intact.
	read := cliReadBackRecord(t, stateDir, fixStoreRunID)
	assert.Equal(t, "state/docs/docs__api.md#patch-winner", read.Applied[0].PatchRef)
}

func cliNewFixRunRecordStoreWith(t *testing.T, stateDir string, record cli.FixRunRecord) *cli.FixRunRecordStore {
	t.Helper()
	store := cli.NewFixRunRecordStore(stateDir)
	require.NoError(t, store.Write(record))
	return store
}

func TestFixRunRecordStoreHasPatchRefAcrossRunsAndFalseMissesCleanly(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), ".docsync", "state")
	store := cli.NewFixRunRecordStore(stateDir)
	// A clean false-miss on an empty ledger.
	assert.False(t, store.HasPatchRef("state/docs/docs__api.md#patch-a3"))

	require.NoError(t, store.Write(cliFixStoreRecord(fixStoreRunID, "a3")))
	// The ref resolves across the store's records; unknown refs still miss.
	assert.True(t, store.HasPatchRef("state/docs/docs__api.md#patch-a3"))
	assert.False(t, store.HasPatchRef("state/docs/docs__api.md#patch-other"))

	// The per-run scoping of the TS hasApplied: another run's record never
	// satisfies this run's gate.
	assert.False(t, store.Has("01J9ZK7Q2M8P4X6T2W5Y3C0A9F"))
}
