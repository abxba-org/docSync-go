package gitpr

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCOV_GITPR_001_TypedErrorMethodSurfaces pins the typed-error families'
// Code/Error/Unwrap — the machine-readable class codes and the docutil house
// rendering every caller (and the CLI's exit-code mapping) depends on.
func TestCOV_GITPR_001_TypedErrorMethodSurfaces(t *testing.T) {
	// commit-assembly typed errors.
	err := AssertProvenancePresent(ProvenanceRef{Record: nil, RunID: "run"})
	var provErr *ProvenanceMissingError
	require.ErrorAs(t, err, &provErr)
	assert.Equal(t, CodeProvenanceMissing, provErr.Code())
	require.Error(t, provErr.Unwrap())
	assert.Equal(t, CodeProvenanceMissing, docutil.CodeOf(provErr))
	assert.Contains(t, provErr.Error(), "INV-10")
	assert.Contains(t, provErr.Error(), "provenance record missing or empty")

	asmErr := &CommitAssemblyError{coded: docutil.New(CodeCommitPlan, "boom")}
	assert.Equal(t, CodeCommitPlan, asmErr.Code())
	require.Error(t, asmErr.Unwrap())
	assert.Equal(t, CodeCommitPlan, docutil.CodeOf(asmErr))
	assert.Contains(t, asmErr.Error(), "boom")

	emptyErr := &EmptyCommitError{coded: docutil.New(CodeEmptyCommit, "nothing staged")}
	assert.Equal(t, CodeEmptyCommit, emptyErr.Code())
	require.Error(t, emptyErr.Unwrap())
	assert.Equal(t, CodeEmptyCommit, docutil.CodeOf(emptyErr))
	assert.Contains(t, emptyErr.Error(), "nothing staged")

	scopeErr := &CommitScopeError{ForeignPaths: []string{"a"}, coded: docutil.New(CodeCommitScope, "foreign")}
	assert.Equal(t, CodeCommitScope, scopeErr.Code())
	assert.Equal(t, []string{"a"}, scopeErr.ForeignPaths)
	require.Error(t, scopeErr.Unwrap())

	// content-guard typed error.
	guardErr := &MachinePrContentGuardError{coded: docutil.New(CodeMachinePrContentGuard, "guard")}
	assert.Equal(t, CodeMachinePrContentGuard, guardErr.Code())
	require.Error(t, guardErr.Unwrap())
	assert.Equal(t, CodeMachinePrContentGuard, docutil.CodeOf(guardErr))

	// pr-platform typed error.
	credErr := &PrCredentialsError{coded: docutil.New(CodePrCredentials, "creds")}
	assert.Contains(t, credErr.Error(), "creds")
	assert.Equal(t, CodePrCredentials, credErr.Code())

	// branch-factory typed errors.
	bnErr := newBranchNameError("branch boom")
	assert.Contains(t, bnErr.Error(), "branch boom")
	assert.Equal(t, CodeBranchName, bnErr.Code())
	assert.Equal(t, CodeBranchName, docutil.CodeOf(bnErr))
	require.Error(t, bnErr.Unwrap())

	dbErr := newDefaultBranchRefError("main", "main")
	assert.Contains(t, dbErr.Error(), "INV-1")
	assert.Equal(t, CodeDefaultBranchRef, dbErr.Code())
	assert.Equal(t, CodeDefaultBranchRef, docutil.CodeOf(dbErr))
	require.Error(t, dbErr.Unwrap())

	// rebase-gate + supersede typed errors.
	gateErr := &RebaseGateError{Msg: "gate boom"}
	assert.Equal(t, "gate boom", gateErr.Error())
	assert.Equal(t, EDOCSYNCRebaseGate, gateErr.Code())
	supErr := &SupersedeError{Msg: "supersede boom"}
	assert.Equal(t, "supersede boom", supErr.Error())
	assert.Equal(t, EDOCSYNCSupersede, supErr.Code())

	// governors + flow config errors.
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(governorConfigError("gov boom")))
	assert.Contains(t, governorConfigError("gov boom").Error(), "gov boom")
	assert.Equal(t, CodePrFilterConfig, docutil.CodeOf(prFilterConfigError("filter boom")))
	assert.Equal(t, EDOCSYNCDocPrFlowConfig, docutil.CodeOf(docPrFlowConfigError("flow boom")))
	assert.Equal(t, EDOCSYNCDocPrFlowInput, docutil.CodeOf(docPrFlowInputError("flow input boom")))
}

// TestCOV_GITPR_002_AssertProvenancePresentRecordShapes pins the INV-10 gate's
// "present" projection: any non-empty object counts, arrays/scalars do not.
func TestCOV_GITPR_002_AssertProvenancePresentRecordShapes(t *testing.T) {
	require.NoError(t, AssertProvenancePresent(ProvenanceRef{
		Record: map[string]any{"schema": "docsync.provenance/v1"}, RunID: "run",
	}))
	require.NoError(t, AssertProvenancePresent(ProvenanceRef{
		Record: &schemas.Provenance{RunID: "run"}, RunID: "run",
	}))
	// Non-empty struct counts as present.
	require.NoError(t, AssertProvenancePresent(ProvenanceRef{
		Record: schemas.Provenance{Schema: "s"}, RunID: "run",
	}))

	// Empty object / nil / scalar / array ⇒ missing.
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: map[string]any{}, RunID: "run"}))
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: nil, RunID: "run"}))
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: []string{"a"}, RunID: "run"}))
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: "rec", RunID: "run"}))
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: 42, RunID: "run"}))
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: true, RunID: "run"}))
	// Present record but an empty run id ⇒ missing (INV-10).
	require.Error(t, AssertProvenancePresent(ProvenanceRef{
		Record: map[string]any{"schema": "s"}, RunID: "",
	}))
	// A typed-nil pointer record ⇒ missing.
	require.Error(t, AssertProvenancePresent(ProvenanceRef{Record: (*schemas.Provenance)(nil), RunID: "run"}))
}

// TestCOV_GITPR_003_AssembleDocCommitRefusals pins every commit-assembly
// refusal branch (fail closed, never a malformed plan).
func TestCOV_GITPR_003_AssembleDocCommitRefusals(t *testing.T) {
	patch := schemas.DocPatchExample
	patch.RunID = "run_asm"
	content := "# doc\n"
	good := AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patch},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents: map[string]string{patch.DocPath: content},
	}
	plan, err := AssembleDocCommit(good)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Files)
	assert.Contains(t, plan.Message, "docs:")
	assert.True(t, plan.Stats.DocPathCount > 0)
	assert.True(t, plan.Stats.FileCount > 0)

	// Provenance absent ⇒ ProvenanceMissingError.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patch},
		DocContents: map[string]string{patch.DocPath: content},
	})
	var provErr *ProvenanceMissingError
	require.ErrorAs(t, err, &provErr)

	// No work at all.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Provenance: ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
	})
	var asmErr *CommitAssemblyError
	require.ErrorAs(t, err, &asmErr)

	// A patch with no DocContents entry.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patch},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents: map[string]string{},
	})
	require.ErrorAs(t, err, &asmErr)

	// Empty applied content is NOT an assembly refusal (a no-op write); it is
	// the write path's *EmptyCommitError — pinned separately.

	// A patch that collides with a state update over the SAME path but carries
	// DIFFERENT content ⇒ an ambiguous plan is refused.
	colliding := patch
	colliding.DocPath = ".docsync/state/collide.json"
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{colliding},
		StateUpdates: []CommitStateUpdate{{RelPath: ".docsync/state/collide.json", Content: "state bytes"}},
		Provenance:   ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents:  map[string]string{".docsync/state/collide.json": content},
	})
	require.ErrorAs(t, err, &asmErr)

	// Two patches over the same doc path with IDENTICAL content collapse into
	// ONE plan file (dedupe, not an ambiguity).
	other := patch
	other.BaseContentHash = "deadbeef"
	deduped, err := AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patch, other},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents: map[string]string{patch.DocPath: content},
	})
	require.NoError(t, err)
	require.Len(t, deduped.Files, 1)

	// A state update outside the sanctioned carriage prefix ⇒ refused.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{patch},
		StateUpdates: []CommitStateUpdate{{RelPath: "secrets.txt", Content: "x"}},
		Provenance:   ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents:  map[string]string{patch.DocPath: content},
	})
	require.ErrorAs(t, err, &asmErr)

	// A carriage update with an empty relPath is refused (it could not name a
	// state file), and a relPath carrying a newline is refused too.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{patch},
		StateUpdates: []CommitStateUpdate{{RelPath: "", Content: "{}"}},
		Provenance:   ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents:  map[string]string{patch.DocPath: content},
	})
	require.ErrorAs(t, err, &asmErr)

	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:      []schemas.DocPatch{patch},
		StateUpdates: []CommitStateUpdate{{RelPath: ".docsync/state/x.json\nmore", Content: "{}"}},
		Provenance:   ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents:  map[string]string{patch.DocPath: content},
	})
	require.ErrorAs(t, err, &asmErr)

	// A patch doc_path carrying a newline is refused the same way.
	broken := patch
	broken.DocPath = "docs/a\nb.md"
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{broken},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_asm"},
		DocContents: map[string]string{"docs/a\nb.md": content},
	})
	require.ErrorAs(t, err, &asmErr)

	// Provenance record present with an EMPTY run id but a nonempty flow run id.
	_, err = AssembleDocCommit(AssembleDocCommitInput{
		Patches: []schemas.DocPatch{patch},
		Provenance: ProvenanceRef{
			Record: map[string]any{"schema": "s"},
			RunID:  "",
		},
		DocContents: map[string]string{patch.DocPath: content},
	})
	require.ErrorAs(t, err, &provErr)
}

// TestCOV_GITPR_004_WriteDocCommitFailures pins the write-path failures: a
// failing apply seam and the post-index scope assert (a foreign staged path is
// a *CommitScopeError — the commit is aborted, never repaired).
func TestCOV_GITPR_004_WriteDocCommitFailures(t *testing.T) {
	patch := schemas.DocPatchExample
	patch.RunID = "run_wdc"
	content := "# doc\n"
	plan, err := AssembleDocCommit(AssembleDocCommitInput{
		Patches:     []schemas.DocPatch{patch},
		Provenance:  ProvenanceRef{Record: map[string]any{"schema": "s"}, RunID: "run_wdc"},
		DocContents: map[string]string{patch.DocPath: content},
	})
	require.NoError(t, err)

	// A nil applier is a config refusal.
	_, err = WriteDocCommit(context.Background(), nil, plan, nil)
	require.Error(t, err)

	// A failing applier propagates.
	_, err = WriteDocCommit(context.Background(), nil, plan, func(string, string) error {
		return assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)

	// A plan carrying no files at all is refused before any git work.
	plan.Files = nil
	_, err = WriteDocCommit(context.Background(), nil, plan, func(string, string) error { return nil })
	require.Error(t, err)
}

// TestCOV_GITPR_005_GitHubRestEpochClock pins the nil-clock default: the
// platform never reads the wall clock (P19); it stamps a constant epoch.
func TestCOV_GITPR_005_GitHubRestEpochClock(t *testing.T) {
	stamp := githubRestEpochClock()
	assert.Equal(t, "1970-01-01T00:00:00.000Z", stamp)
	// Deterministic: repeated calls are identical.
	assert.Equal(t, stamp, githubRestEpochClock())
	// Reachable through the platform constructor (nil clock ⇒ epoch clock).
	platform := CreateGitHubRestPlatform(GitHubRestPlatformDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
	})
	assert.NotNil(t, platform)
	// The default clock is what stamps `updated_at` on created PRs; the same
	// constructor with a custom BaseUrl still stamps it.
	assert.NotEmpty(t, stamp)
}

// TestCOV_GITPR_006_GitHubRestHelperProjections pins the pure projection
// helpers behind the REST wire (the bounded-evidence and error-message rules).
func TestCOV_GITPR_006_GitHubRestHelperProjections(t *testing.T) {
	// githubStringifyRecord: round-trip, and the unserializable projection.
	assert.Equal(t, `{"a":1}`, githubStringifyRecord(map[string]any{"a": 1}))
	assert.Equal(t, "<unserializable>", githubStringifyRecord(map[string]any{"a": math.NaN()}))

	// githubFiniteInt: only a numeric value >= 1 projects.
	value, ok := githubFiniteInt(float64(7))
	assert.True(t, ok)
	assert.Equal(t, 7, value)
	_, ok = githubFiniteInt(float64(0))
	assert.False(t, ok)
	_, ok = githubFiniteInt(float64(-2))
	assert.False(t, ok)
	_, ok = githubFiniteInt("7")
	assert.False(t, ok)
	_, ok = githubFiniteInt(nil)
	assert.False(t, ok)
	_, ok = githubFiniteInt(json.Number("9"))
	assert.True(t, ok)
	_, ok = githubFiniteInt(json.Number("bad"))
	assert.False(t, ok)

	// githubMessageOf: absent / non-JSON / non-string ⇒ nil.
	message := githubMessageOf(`{"message":"boom"}`)
	require.NotNil(t, message)
	assert.Equal(t, "boom", *message)
	assert.Nil(t, githubMessageOf(`{}`))
	assert.Nil(t, githubMessageOf(`not json`))
	assert.Nil(t, githubMessageOf(`{"message":42}`))

	// githubNumericHeader: canonical and underscore forms.
	number, ok := githubNumericHeader(map[string]string{"retry-after": "17"}, "retry-after")
	assert.True(t, ok)
	assert.Equal(t, float64(17), number)
	number, ok = githubNumericHeader(map[string]string{"retry_after": "3"}, "retry-after")
	assert.True(t, ok)
	assert.Equal(t, float64(3), number)
	_, ok = githubNumericHeader(map[string]string{"other": "3"}, "retry-after")
	assert.False(t, ok)
	_, ok = githubNumericHeader(map[string]string{"retry-after": "abc"}, "retry-after")
	assert.False(t, ok)

	// githubBodyExcerpt: bounded at 500 runes.
	assert.Equal(t, "short", githubBodyExcerpt("short"))
	long := strings.Repeat("x", 600)
	assert.Len(t, githubBodyExcerpt(long), 500)

	// githubStringField / githubNestedString.
	record := map[string]any{"state": "open", "title": 42}
	assert.Equal(t, "open", githubStringField(record, "state"))
	assert.Equal(t, "", githubStringField(record, "missing"))
	assert.Equal(t, "", githubStringField(record, "title"))
	nested := map[string]any{"head": map[string]any{"ref": "docsync/x"}}
	assert.Equal(t, "docsync/x", githubNestedString(nested, "head", "ref"))
	assert.Equal(t, "", githubNestedString(nested, "base", "ref"))
}

// TestCOV_GITPR_007_LinkHeaderHasNext pins the Link-header pagination probe
// (rel=next AND an http(s) target).
func TestCOV_GITPR_007_LinkHeaderHasNext(t *testing.T) {
	assert.False(t, linkHeaderHasNext(""))
	assert.True(t, linkHeaderHasNext(`<https://api.github.com/x?page=2>; rel="next"`))
	assert.False(t, linkHeaderHasNext(`<https://api.github.com/x?page=2>; rel="last"`))
	assert.False(t, linkHeaderHasNext(`<https://api.github.com/x?page=2>`))
	assert.False(t, linkHeaderHasNext(`<ftp://x>; rel="next"`))
	// A next link after an ignored segment still resolves.
	assert.True(t, linkHeaderHasNext(
		`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`))
	// An empty segment is skipped.
	assert.True(t, linkHeaderHasNext(
		`, <https://api.github.com/x?page=2>; rel="next"`))
	// A rel-less segment is skipped.
	assert.False(t, linkHeaderHasNext(`<https://api.github.com/x>`))
}

// TestCOV_GITPR_008_GovernorConfigAndGroupHelpers pins the governors'
// fail-closed config surface and the pure concurrency-group helpers.
func TestCOV_GITPR_008_GovernorConfigAndGroupHelpers(t *testing.T) {
	// OpenPrCapGovernor: out-of-range maxOpen.
	_, err := NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 0})
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))
	_, err = NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 11})
	require.Error(t, err)

	governor, err := NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: 2})
	require.NoError(t, err)
	result, err := governor.Evaluate(0)
	require.NoError(t, err)
	assert.True(t, result.Allowed)
	result, err = governor.Evaluate(2)
	require.NoError(t, err)
	assert.False(t, result.Allowed)
	assert.True(t, result.Downgrade)
	assert.Equal(t, PatchReadyMessage, result.DowngradeMsg)
	// A negative open count is a config refusal.
	_, err = governor.Evaluate(-1)
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))

	// HourlyBurstLimiter: maxPerHour < 1 and a nil clock both fail closed.
	_, err = NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 0}, covFixedClock{}, nil)
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))
	_, err = NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 1}, nil, nil)
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))

	// assertValidGroupInput: empty repo, negative PR.
	require.Error(t, assertValidGroupInput("", nil))
	zero := 0
	require.Error(t, assertValidGroupInput("acme/widgets", &zero))
	require.NoError(t, assertValidGroupInput("acme/widgets", nil))

	group, err := DocPrConcurrencyGroup("acme/widgets", nil)
	require.NoError(t, err)
	assert.Equal(t, "docsync-acme/widgets-local", group)
	number := 481
	group, err = DocPrConcurrencyGroup("acme/widgets", &number)
	require.NoError(t, err)
	assert.Equal(t, "docsync-acme/widgets-481", group)
	_, err = DocPrConcurrencyGroup("", nil)
	require.Error(t, err)

	action, err := SupersedeInFlight(group, Supersedable{}, Supersedable{})
	require.NoError(t, err)
	assert.Equal(t, SupersedeInFlightNew, action)
	action, err = SupersedeInFlight(group,
		Supersedable{IdempotencyKey: "k1"}, Supersedable{IdempotencyKey: "k2"})
	require.NoError(t, err)
	assert.Equal(t, SupersedeInFlightSuperseded, action)
	// An empty group is a config refusal.
	_, err = SupersedeInFlight("", Supersedable{}, Supersedable{})
	require.Error(t, err)
}

// TestCOV_GITPR_009_RealBurstDelayAndLimiterBoundaries pins the real-delay
// default plus the limiter's ctx-cancellation and no-progress guards.
func TestCOV_GITPR_009_RealBurstDelayAndLimiterBoundaries(t *testing.T) {
	ctx := context.Background()

	// realBurstDelay: a non-positive wait resolves immediately through the
	// ctx-error branch (nil for a live ctx, the ctx error once cancelled).
	require.NoError(t, realBurstDelay(ctx, 0))
	require.NoError(t, realBurstDelay(ctx, -5))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, realBurstDelay(canceled, 0), context.Canceled)
	require.ErrorIs(t, realBurstDelay(canceled, 10), context.Canceled)
	// A live ctx with a real 1ms wait completes successfully.
	require.NoError(t, realBurstDelay(context.Background(), 1))

	// A nil delay ⇒ the real delay is the fallback.
	limiter, err := NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 1}, covFixedClock{}, nil)
	require.NoError(t, err)
	require.NotNil(t, limiter)

	// In-window count reflects the ring; eviction ages out strictly-old stamps.
	assert.Equal(t, 0, limiter.InWindowCount())
	admitted, err := limiter.Admit(ctx)
	require.NoError(t, err)
	assert.True(t, admitted.Admitted)
	assert.False(t, admitted.Queued)
	assert.Equal(t, 1, limiter.InWindowCount())

	// A ctx cancelled while over budget fails loud (never a silent admit).
	limiter2, err := NewHourlyBurstLimiter(BurstLimiterConfig{MaxPerHour: 1}, covFixedClock{}, nil)
	require.NoError(t, err)
	_, err = limiter2.Admit(ctx)
	require.NoError(t, err)
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	queued, err := limiter2.Admit(canceledCtx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, queued.Admitted)
	assert.True(t, queued.Queued)

	// No progress at all (a stuck clock) fails closed rather than spinning.
	limiter3, err := NewHourlyBurstLimiter(
		BurstLimiterConfig{MaxPerHour: 1}, covStuckClock{},
		func(context.Context, int64) error { return nil })
	require.NoError(t, err)
	_, err = limiter3.Admit(ctx)
	require.NoError(t, err)
	stuck, err := limiter3.Admit(ctx)
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))
	assert.False(t, stuck.Admitted)
	assert.True(t, stuck.Queued)
}

// TestCOV_GITPR_010_NoopDropGitDoubleNeverReads pins the simulation's git
// double: a CatFile probe is a typed refusal, never a read — the simulation
// only ever receives head/base CONTENT.
func TestCOV_GITPR_010_NoopDropGitDoubleNeverReads(t *testing.T) {
	read, err := noopDropNeverReadGit{}.CatFile(context.Background(), "rev", "path")
	require.Error(t, err)
	assert.Empty(t, read)
	assert.Equal(t, docutil.CodeInternal, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "git must never be read")

	// The same double again — the refusal is deterministic and carries no
	// fabricated content.
	read2, err2 := noopDropNeverReadGit{}.CatFile(context.Background(), "rev", "path")
	require.Error(t, err2)
	assert.Empty(t, read2)
	assert.Equal(t, err.Error(), err2.Error())
}

// TestCOV_GITPR_011_PrFilterConfigAndModes pins the filter's fail-closed input
// surface plus the manifest-derived mode projections.
func TestCOV_GITPR_011_PrFilterConfigAndModes(t *testing.T) {
	_, err := CreatePrFilter(PrFilterDeps{})
	require.Error(t, err)
	assert.Equal(t, CodePrFilterConfig, docutil.CodeOf(err))

	repoRoot := t.TempDir()
	filter, err := CreatePrFilter(PrFilterDeps{Git: gitxClientFor(t, repoRoot)})
	require.NoError(t, err)
	require.NotNil(t, filter)

	auto := schemas.DocModeAuto
	locked := schemas.DocModeLocked

	// effectiveMode: the entry wins, then the default, then suggest.
	manifest := &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{{
		Path: "docs/a.md", Mode: &auto,
	}}}
	assert.Equal(t, schemas.DocModeAuto, effectiveMode(manifest.Documents[0], manifest))

	withDefault := &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{{
		Path: "docs/a.md",
	}}, Defaults: &schemas.ManifestDefaults{Mode: &locked}}
	assert.Equal(t, schemas.DocModeLocked, effectiveMode(withDefault.Documents[0], withDefault))

	withoutMode := &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{{
		Path: "docs/a.md",
	}}}
	assert.Equal(t, schemas.DocModeSuggest, effectiveMode(withoutMode.Documents[0], withoutMode))

	// prFilterIsProtectedMode.
	assert.True(t, prFilterIsProtectedMode(string(schemas.DocModeLocked)))
	assert.True(t, prFilterIsProtectedMode(string(schemas.DocModeGenerated)))
	assert.False(t, prFilterIsProtectedMode(string(schemas.DocModeAuto)))
	assert.False(t, prFilterIsProtectedMode(string(schemas.DocModeSuggest)))
	assert.False(t, prFilterIsProtectedMode(""))

	// DerivePrPathAllowlist: writable vs protected split.
	allowlist := DerivePrPathAllowlist(&schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{
		{Path: "docs/w.md", Mode: &auto},
		{Path: "docs/p.md", Mode: &locked},
		{Path: "docs/d.md"}, // defaults absent ⇒ suggest ⇒ writable
	}})
	assert.True(t, allowlist.WritablePaths["docs/w.md"])
	assert.True(t, allowlist.WritablePaths["docs/d.md"])
	assert.True(t, allowlist.ProtectedPaths["docs/p.md"])
	assert.False(t, allowlist.WritablePaths["docs/p.md"])
}

// TestCOV_GITPR_012_FilterBranchDiffInputRefusals pins assertFilterInput's
// every failure branch.
func TestCOV_GITPR_012_FilterBranchDiffInputRefusals(t *testing.T) {
	repoRoot := t.TempDir()
	filter, err := CreatePrFilter(PrFilterDeps{Git: gitxClientFor(t, repoRoot)})
	require.NoError(t, err)

	manifest := &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{}}
	cases := []struct {
		name  string
		input FilterBranchDiffInput
	}{
		{"empty repoRoot", FilterBranchDiffInput{RepoRoot: "", Base: "main", Branch: "b", Manifest: manifest}},
		{"relative repoRoot", FilterBranchDiffInput{RepoRoot: "relative", Base: "main", Branch: "b", Manifest: manifest}},
		{"empty base", FilterBranchDiffInput{RepoRoot: repoRoot, Base: "", Branch: "b", Manifest: manifest}},
		{"empty branch", FilterBranchDiffInput{RepoRoot: repoRoot, Base: "main", Branch: "", Manifest: manifest}},
		{"nil manifest", FilterBranchDiffInput{RepoRoot: repoRoot, Base: "main", Branch: "b", Manifest: nil}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := filter.FilterBranchDiff(context.Background(), testCase.input)
			require.Error(t, err)
			assert.Equal(t, CodePrFilterConfig, docutil.CodeOf(err))
		})
	}
}

// gitxClientFor builds a GitClient bound to repoRoot.
func gitxClientFor(t *testing.T, repoRoot string) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repoRoot})
	require.NoError(t, err)
	return client
}

// covFixedClock is a deterministic millisecond clock.
type covFixedClock struct{}

func (covFixedClock) NowMs() int64 { return 0 }

// covStuckClock never advances.
type covStuckClock struct{}

func (covStuckClock) NowMs() int64 { return 0 }
