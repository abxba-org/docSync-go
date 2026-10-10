// Black-box port of packages/core/test/audit-loop-b.test.ts (P11-T19; ARCH
// §7.2 Loop B — Background drift audit, §6.3 audit-cursor; PLAN §12.5 V-P11-8).
//
// Pins:
//   - V-P11-8 audit-cursor idempotency: the cursor advances after a full run;
//     an identical re-run yields zero duplicate findings for the same claim ID.
//   - P11-T19 chunking/scope: per-document chunks over the manifest allowlist
//     ONLY (D7 — no blanket scanning), through the REAL fail-closed manifest
//     loader over a temp git repo.
//   - P11-T19 crash safety: a crash between chunks leaves the cursor at the
//     last completed chunk; the re-run re-audits only the unfinished chunks
//     and emits no duplicates for completed work.
//   - P11-T19 CAS discipline: a cursor CAS loser is reported re-run-required,
//     never blind-written (R15, ARCH §6.3).
//   - P11-T19 fail-closed ports: cursor read / manifest / position failures
//     degrade to a typed abstain; a schema-drift finding from the port is a
//     typed chunk failure, never a silent drop.
//
// Fixtures: the REAL StateStore over a temp `.docsync/state` dir; real system
// git for the manifest-scope case (pinned identity/dates, repo-local
// core.autocrlf=false + core.eol=lf before any file is written — DX-P11-3).
// The engine's drift predicates are injectable doubles.

package audit_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/stretchr/testify/require"
)

// loopBDocs is the deterministic manifest doc list (already sorted, so the
// chunk order is deterministic under the pool).
var loopBDocs = []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}

/* ------------------------------------------------------------ fixture kit */

// newStateStore builds the REAL state store over a temp `.docsync/state` dir
// (registry-enforced IO).
func newStateStore(t *testing.T) *state.StateStore {
	t.Helper()
	store, err := state.NewStateStore(state.StateStoreOptions{
		StateDir: filepath.Join(t.TempDir(), schemas.DocSyncDir, "state"),
	})
	require.NoError(t, err)
	return store
}

// stateCursorPort satisfies audit.AuditCursorPort straight from the real
// StateStore (the ARCH §6.3 wiring shape).
type stateCursorPort struct {
	store *state.StateStore
}

func (p stateCursorPort) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return p.store.ReadAuditCursor()
}

func (p stateCursorPort) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return p.store.WriteAuditCursor(cursor, expectedHash)
}

// portCall records one per-doc port invocation (chunk granularity + cursor
// input).
type portCall struct {
	DocPath        string
	CursorPosition *string
}

// portDouble records per-doc port invocations (mutex-guarded: the engine runs
// chunks concurrently, and the suite runs under -race). ErrFor maps a doc path
// to the failure message that port invocation returns (the TS crashing
// predicate); clearing an entry repairs the chunk.
type portDouble struct {
	mu     sync.Mutex
	Calls  []portCall
	ErrFor map[string]string
	port   audit.AuditDocPort
}

// makeAuditPort builds the recording port double around a behavior.
func makeAuditPort(behavior func(docPath string) []audit.DriftFinding) *portDouble {
	double := &portDouble{}
	double.port = func(_ context.Context, docPath string, cursor *schemas.AuditCursor) ([]audit.DriftFinding, error) {
		var position *string
		if cursor != nil {
			position = &cursor.Position
		}
		double.mu.Lock()
		double.Calls = append(double.Calls, portCall{DocPath: docPath, CursorPosition: position})
		failure, failing := double.ErrFor[docPath]
		double.mu.Unlock()
		if failing {
			return nil, errors.New(failure)
		}
		return behavior(docPath), nil
	}
	return double
}

// docPaths returns the recorded port invocation paths in call order.
func (d *portDouble) docPaths() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.Calls))
	for _, call := range d.Calls {
		out = append(out, call.DocPath)
	}
	return out
}

// buildDeps wires the engine deps over the real state store and the doubles.
func buildDeps(store *state.StateStore, manifestDocs []string, port audit.AuditDocPort) audit.IncrementalAuditDeps {
	return audit.IncrementalAuditDeps{
		Cursor: stateCursorPort{store: store},
		ManifestDocs: func(context.Context) ([]string, error) {
			return manifestDocs, nil
		},
		Position: func(context.Context) (string, error) { return auditHead, nil },
		AuditDoc: port,
	}
}

// parseStoredPosition reads and decodes the persisted cursor position.
func parseStoredPosition(t *testing.T, store *state.StateStore) *audit.ParsedAuditPosition {
	t.Helper()
	read, err := store.ReadAuditCursor()
	require.NoError(t, err)
	require.True(t, read.Exists)
	return audit.ParseAuditPosition(read.Content.Position)
}

// runAudit runs the engine with the pinned clock.
func runAudit(t *testing.T, deps audit.IncrementalAuditDeps, concurrency int) audit.AuditRunResult {
	t.Helper()
	return audit.RunIncrementalAudit(context.Background(), deps, audit.AuditRunOptions{
		Now:         auditNow,
		Concurrency: intPtr(concurrency),
	})
}

/* ------------------------------------------------------------------- tests */

func TestVP11_8_CursorAdvancesAfterFullRunAndReRunIsIdempotent(t *testing.T) {
	store := newStateStore(t)

	// Two docs report the SAME claim (cross-doc duplicate) plus per-doc
	// claims.
	first := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{
			loopBFinding("claim-shared", docPath, "c-"+docPath),
			loopBFinding("claim-"+docPath, docPath, "c-"+docPath),
		}
	})
	result1 := runAudit(t, buildDeps(store, loopBDocs, first.port), 2)

	require.Equal(t, audit.AuditRunCompleted, result1.Status)
	require.Nil(t, result1.Detail)
	require.Equal(t, []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}, result1.AuditedDocs)

	// Deduplicated by claim ID: claim-shared reported EXACTLY ONCE across the
	// four per-doc occurrences, evidence commits merged (union), latest-wins
	// body.
	require.Len(t, result1.Findings, 5)
	shared := findingsWithClaim(result1.Findings, "claim-shared")
	require.Len(t, shared, 1)
	require.Equal(t,
		[]string{"c-docs/a.md", "c-docs/b.md", "c-docs/c.md", "c-docs/d.md"},
		shared[0].Evidence.Commits)

	// Cursor advanced: done=4, anchored at this run's position.
	cursor1 := parseStoredPosition(t, store)
	require.NotNil(t, cursor1)
	require.Equal(t, &audit.ParsedAuditPosition{
		Since:    auditHead,
		Done:     4,
		ListHash: audit.ChunkListHash([]string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}),
	}, cursor1)
	require.True(t, result1.Cursor.Advanced)

	// Identical re-run over the same state: the cursor resumes past every
	// chunk, zero docs re-audited, zero findings — so the post-dedup finding
	// set adds NO duplicate for the same claim ID.
	second := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	result2 := runAudit(t, buildDeps(store, loopBDocs, second.port), 2)
	require.Equal(t, audit.AuditRunCompleted, result2.Status)
	require.Empty(t, result2.Findings)
	require.Empty(t, result2.AuditedDocs)
	require.Equal(t, []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}, result2.SkippedDocs)
	require.False(t, result2.Cursor.Advanced)
	require.Empty(t, second.docPaths())

	// The union across both runs holds every claim exactly once.
	for _, count := range countByClaim(append(append([]audit.DriftFinding{}, result1.Findings...), result2.Findings...)) {
		require.Equal(t, 1, count)
	}

	// The stored cursor is byte-stable across the no-op re-run.
	require.Equal(t, cursor1, parseStoredPosition(t, store))
}

func TestP11T19_AuditIsChunkedPerDocumentAndManifestScoped(t *testing.T) {
	repo := trackGitRepo(t)
	writeFile(t, filepath.Join(repo, ".docsync", "manifest.yml"), manifestYAML)
	// Out-of-manifest decoy on disk: the audit must NEVER touch it (D7).
	writeFile(t, filepath.Join(repo, "docs", "z.md"), "# decoy\n")
	runGit(t, repo, "add", "-A")
	runGitIdentity(t, repo, "commit", "-q", "-m", "fixture: manifest + docs")

	// The REAL fail-closed manifest loader (the W5b2 wiring shape for the
	// port).
	result, err := validate.LoadAndValidateManifest(repo, validate.ManifestValidateOptions{})
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.NotNil(t, result.Manifest)
	manifestDocs := make([]string, 0, len(result.Manifest.Documents))
	for _, entry := range result.Manifest.Documents {
		manifestDocs = append(manifestDocs, entry.Path)
	}

	store := newStateStore(t)
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	engineResult := runAudit(t, buildDeps(store, manifestDocs, double.port), 2)

	require.Equal(t, audit.AuditRunCompleted, engineResult.Status)
	// Per-document chunking: exactly ONE port invocation per manifest doc.
	require.Len(t, double.Calls, len(manifestDocs))
	require.Equal(t,
		[]string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"},
		sortedStrings(double.docPaths()))
	// Every chunk received the SAME previous-cursor input (fresh run: none).
	for _, call := range double.Calls {
		require.Nil(t, call.CursorPosition)
	}
	// Nothing outside the manifest allowlist was ever scanned.
	require.NotContains(t, double.docPaths(), "docs/z.md")
	require.Equal(t,
		[]string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}, engineResult.AuditedDocs)
}

func TestP11T19_CrashBetweenChunksLeavesCursorAtLastCompletedChunk(t *testing.T) {
	store := newStateStore(t)
	// Doc 3 of 4 throws; concurrency 1 makes the chunk order deterministic.
	var repaired bool
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	double.mu.Lock()
	double.ErrFor = map[string]string{}
	double.mu.Unlock()

	armCrash := func() {
		double.mu.Lock()
		defer double.mu.Unlock()
		if !repaired {
			double.ErrFor["docs/c.md"] = "boom: drift predicate crashed"
		} else {
			delete(double.ErrFor, "docs/c.md")
		}
	}
	// The crashing predicate arms the failure for as long as it is unrepaired.
	armCrash()

	result1 := runAudit(t, buildDeps(store, loopBDocs, double.port), 1)

	require.Equal(t, audit.AuditRunPartial, result1.Status)
	require.Contains(t, *result1.Detail, "docs/c.md")
	require.Contains(t, *result1.Detail, "boom: drift predicate crashed")
	// Chunks 1-2 completed and are persisted; the failed chunk 3 bounds the
	// cursor.
	require.Equal(t, []string{"docs/a.md", "docs/b.md"}, result1.AuditedDocs)
	require.Equal(t, []*string{strPtr("claim-docs/a.md"), strPtr("claim-docs/b.md")},
		claimIDsOf(result1.Findings))
	cursor1 := parseStoredPosition(t, store)
	require.Equal(t, 2, cursor1.Done)
	require.Equal(t, audit.ChunkListHash([]string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md"}),
		cursor1.ListHash)
	// Chunk 4 was never started after the crash (bounded pool short-circuit).
	require.Equal(t, []string{"docs/a.md", "docs/b.md", "docs/c.md"}, double.docPaths())
	require.True(t, result1.Cursor.Advanced)

	// Re-run after repair: ONLY the unfinished chunks 3-4 are re-audited;
	// completed work is neither re-invoked nor re-emitted (no duplicates).
	repaired = true
	armCrash()
	result2 := runAudit(t, buildDeps(store, loopBDocs, double.port), 1)
	require.Equal(t, audit.AuditRunCompleted, result2.Status)
	require.Equal(t, []string{"docs/a.md", "docs/b.md"}, result2.SkippedDocs)
	require.Equal(t, []string{"docs/c.md", "docs/d.md"}, result2.AuditedDocs)
	require.Equal(t, []*string{strPtr("claim-docs/c.md"), strPtr("claim-docs/d.md")},
		claimIDsOf(result2.Findings))
	for _, count := range countByClaim(append(append([]audit.DriftFinding{}, result1.Findings...), result2.Findings...)) {
		require.Equal(t, 1, count)
	}
	cursor2 := parseStoredPosition(t, store)
	require.Equal(t, 4, cursor2.Done)
	require.Equal(t, auditHead, cursor2.Since)
}

func TestP11T19_CursorCASLoserIsReportedReRunRequiredNeverBlindWritten(t *testing.T) {
	store := newStateStore(t)
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	base := buildDeps(store, loopBDocs, double.port)

	// A concurrent competing writer wins the cursor between this run's read
	// and its first CAS write (the winner uses the correct expected hash).
	winner := schemas.AuditCursor{
		Position:  audit.EncodeAuditPosition("winner-since", 99, "deadbeefcafe"),
		UpdatedAt: auditNow,
	}
	var competed bool
	var mu sync.Mutex
	losingPort := &casCompetingCursorPort{
		store: store,
		onWrite: func(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
			mu.Lock()
			first := !competed
			competed = true
			mu.Unlock()
			if first {
				// The concurrent winner's CAS write (correct expectation)
				// lands first.
				winnerWrite, err := store.WriteAuditCursor(winner, expectedHash)
				require.NoError(t, err)
				require.True(t, winnerWrite.OK)
			}
			// The engine's own write still carries its STALE expectation →
			// conflict.
			return store.WriteAuditCursor(cursor, expectedHash)
		},
	}

	result := runAudit(t, audit.IncrementalAuditDeps{
		Cursor:       losingPort,
		ManifestDocs: base.ManifestDocs,
		Position:     base.Position,
		AuditDoc:     base.AuditDoc,
	}, 2)

	require.Equal(t, audit.AuditRunReRunRequired, result.Status)
	require.Contains(t, *result.Detail, "CAS conflict")
	// Findings are data (ARCH §7.2): every chunk that SUCCEEDED before the
	// CAS loser was observed reports its findings, deduplicated by claim ID
	// and never duplicated. The Go engine runs a真 parallel worker pool, so
	// exactly how many chunks land before the conflict is scheduling-dependent
	// (the TS single-threaded chain happens to run all four before the
	// conflict surfaces); the PINNED invariant is the subset/once-only shape,
	// not a specific count — see CONVENTIONS: no silent behavior change, the
	// documented difference is scheduling, not semantics.
	reported := sortedClaimIDs(claimIDsOf(result.Findings))
	all := []*string{
		strPtr("claim-docs/a.md"), strPtr("claim-docs/b.md"),
		strPtr("claim-docs/c.md"), strPtr("claim-docs/d.md"),
	}
	require.NotEmpty(t, reported)
	require.LessOrEqual(t, len(reported), len(all))
	for _, id := range reported {
		require.Contains(t, all, id)
	}
	seen := map[string]int{}
	for _, id := range reported {
		seen[*id]++
		require.Equal(t, 1, seen[*id], "claim ID dedup: %s reported more than once", *id)
	}
	stored, err := store.ReadAuditCursor()
	require.NoError(t, err)
	require.True(t, stored.Exists)
	require.Equal(t, winner.Position, stored.Content.Position)
	require.False(t, result.Cursor.Advanced)
	require.Nil(t, result.Cursor.Position)
}

func TestP11T19_PortFailuresDegradeToTypedAbstains(t *testing.T) {
	store := newStateStore(t)
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})

	brokenCursor := buildDeps(store, loopBDocs, double.port)
	brokenCursor.Cursor = &failingCursorPort{store: store}
	abstained := runAudit(t, brokenCursor, 1)
	require.Equal(t, audit.AuditRunAbstained, abstained.Status)
	require.Contains(t, *abstained.Detail, "audit-cursor read failed")
	require.Contains(t, *abstained.Detail, "state store offline")
	require.Empty(t, abstained.Findings)
	require.False(t, abstained.Cursor.Advanced)
	require.Empty(t, double.docPaths())

	brokenManifest := buildDeps(store, loopBDocs, double.port)
	brokenManifest.ManifestDocs = func(context.Context) ([]string, error) {
		return nil, errors.New("manifest IO exploded")
	}
	manifestAbstain := runAudit(t, brokenManifest, 1)
	require.Equal(t, audit.AuditRunAbstained, manifestAbstain.Status)
	require.Contains(t, *manifestAbstain.Detail, "manifest scope read failed")
	require.Contains(t, *manifestAbstain.Detail, "manifest IO exploded")
	require.Empty(t, double.docPaths())

	brokenPosition := buildDeps(store, loopBDocs, double.port)
	brokenPosition.Position = func(context.Context) (string, error) {
		return "", errors.New("position offline")
	}
	positionAbstain := runAudit(t, brokenPosition, 1)
	require.Equal(t, audit.AuditRunAbstained, positionAbstain.Status)
	require.Contains(t, *positionAbstain.Detail, "audit position resolution failed")

	emptyManifest := buildDeps(store, nil, double.port)
	emptyAbstain := runAudit(t, emptyManifest, 1)
	require.Equal(t, audit.AuditRunAbstained, emptyAbstain.Status)
	require.Contains(t, *emptyAbstain.Detail, "allowlist scope")
}

func TestP11T19_SchemaDriftFindingIsATypedChunkFailure(t *testing.T) {
	store := newStateStore(t)
	// An invalid finding: no id, no evidence, no timestamp (the TS
	// `{ doc_path, kind: 'broken' }` shape).
	double := makeAuditPort(func(docPath string) []audit.DriftFinding {
		if docPath == "docs/a.md" {
			return []audit.DriftFinding{{DocPath: "docs/a.md", Kind: "broken"}}
		}
		return []audit.DriftFinding{loopBFinding("claim-"+docPath, docPath, "c-"+docPath)}
	})
	result := runAudit(t, buildDeps(store, loopBDocs, double.port), 1)

	require.Equal(t, audit.AuditRunPartial, result.Status)
	require.Contains(t, *result.Detail, "invalid DriftFinding")
	require.Contains(t, *result.Detail, "docs/a.md")
	// The cursor never advanced past the failed chunk (fail closed).
	require.False(t, result.Cursor.Advanced)
	require.Nil(t, result.Cursor.Position)
	require.Empty(t, result.Findings)
	// The failed chunk is the first chunk: with concurrency 1 nothing else
	// started (findings are data, and none were produced).
	require.Empty(t, result.AuditedDocs)
}

/* -------------------------------------------------- cursor position codec */

func TestP11T19_AuditPositionCodecRoundTrips(t *testing.T) {
	encoded := audit.EncodeAuditPosition("81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5", 7, "abc123def456")
	require.Equal(t,
		"loopb:v1;since=81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5;done=7;list=abc123def456",
		encoded)

	parsed := audit.ParseAuditPosition(encoded)
	require.NotNil(t, parsed)
	require.Equal(t, &audit.ParsedAuditPosition{
		Since:    "81af92e9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5",
		Done:     7,
		ListHash: "abc123def456",
	}, parsed)

	// The `since` value is percent-encoded so the delimiters stay unambiguous.
	encoded = audit.EncodeAuditPosition("a;b=c/d", 0, "list")
	parsed = audit.ParseAuditPosition(encoded)
	require.NotNil(t, parsed)
	require.Equal(t, "a;b=c/d", parsed.Since)
	require.Equal(t, 0, parsed.Done)

	// Foreign formats are refused (nil), never parsed loosely.
	require.Nil(t, audit.ParseAuditPosition("other:v1;since=x;done=1;list=abc"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;done=1;list=abc"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=x;done=1"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=x;done=-1;list=abc"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=x;done=abc;list=abc"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=x;done=1;list="))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=%zz;done=1;list=abc"))
	require.Nil(t, audit.ParseAuditPosition("loopb:v1;since=x;done=1;list=abc;extra=1"))
}

func TestP11T19_ChunkListHashIsStableAndOrderSensitive(t *testing.T) {
	docs := []string{"docs/a.md", "docs/b.md"}
	require.Equal(t, audit.ChunkListHash(docs), audit.ChunkListHash([]string{"docs/a.md", "docs/b.md"}))
	require.NotEqual(t,
		audit.ChunkListHash(docs),
		audit.ChunkListHash([]string{"docs/b.md", "docs/a.md"}))
	require.Len(t, audit.ChunkListHash(docs), 12)
}

/* ------------------------------------------------------- failing doubles */

// failingCursorPort refuses every read (the TS broken cursor port).
type failingCursorPort struct {
	store *state.StateStore
}

func (p *failingCursorPort) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return state.StateRead[*schemas.AuditCursor]{}, errors.New("state store offline")
}

func (p *failingCursorPort) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return p.store.WriteAuditCursor(cursor, expectedHash)
}

// casCompetingCursorPort wraps the real cursor port so the FIRST engine write
// loses to a concurrent winner (the TS concurrent-writer wrapper).
type casCompetingCursorPort struct {
	store   *state.StateStore
	onWrite func(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error)
}

func (p *casCompetingCursorPort) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return p.store.ReadAuditCursor()
}

func (p *casCompetingCursorPort) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return p.onWrite(cursor, expectedHash)
}

/* ----------------------------------------------------------------- helpers */

// findingsWithClaim selects the findings carrying the given claim id.
func findingsWithClaim(findings []audit.DriftFinding, claimID string) []audit.DriftFinding {
	out := []audit.DriftFinding{}
	for _, finding := range findings {
		if finding.ClaimID != nil && *finding.ClaimID == claimID {
			out = append(out, finding)
		}
	}
	return out
}

// countByClaim counts findings per dedup key (the TS countByClaim helper).
func countByClaim(findings []audit.DriftFinding) map[string]int {
	counts := map[string]int{}
	for _, finding := range findings {
		key := "unclaimed:" + finding.DocPath + "|" + finding.Kind
		if finding.ClaimID != nil {
			key = *finding.ClaimID
		}
		counts[key]++
	}
	return counts
}

// sortedClaimIDs sorts the claim ids (the TS `.map(...).sort()`).
func sortedClaimIDs(ids []*string) []*string {
	out := append([]*string{}, ids...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && claimKey(out[j]) < claimKey(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func claimKey(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

// sortedStrings returns a sorted copy (locale-independent byte order).
func sortedStrings(values []string) []string {
	out := append([]string{}, values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

/* ------------------------------------------------------ git fixture (D7) */

// manifestYAML is the four-document manifest of the manifest-scope test (TS
// MANIFEST_YAML).
const manifestYAML = `version: 1
documents:
  - path: docs/a.md
    sources:
      - "src/a/**"
  - path: docs/b.md
    sources:
      - "src/b/**"
  - path: docs/c.md
    sources:
      - "src/c/**"
  - path: docs/d.md
    sources:
      - "src/d/**"
`

// fixtureGitEnv is the deterministic, isolated git environment (TS GIT_ENV:
// no user/system config, fixed identity + dates).
func fixtureGitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"TERM=dumb",
	}
}

// gitIdentityArgs pins the commit identity per invocation.
var gitIdentityArgs = []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}

// trackGitRepo creates the deterministic temp git repo (gitpr-rebase.test.ts
// init pattern, DX-P11-3) and returns its root.
func trackGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-b", "main")
	// Windows platform-port (DX-P11-3): pin LF in-repo BEFORE any file write.
	runGit(t, root, "config", "core.autocrlf", "false")
	runGit(t, root, "config", "core.eol", "lf")
	return root
}

// runGit runs a raw git command in the fixture (fixtures only).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

// runGitIdentity runs a fixture git command with the pinned commit identity.
func runGitIdentity(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append(append([]string{"-C", dir}, gitIdentityArgs...), args...)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

// gitStatus returns the porcelain status lines (the clean-run assertions).
func gitStatus(t *testing.T, dir string) []string {
	t.Helper()
	out := runGitOut(t, dir, "status", "--porcelain")
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " "))
		}
	}
	return lines
}

// runGitOut runs a raw fixture git command and returns its stdout.
func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = fixtureGitEnv()
	out, err := cmd.Output()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return string(out)
}

// writeFile writes one fixture file, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
