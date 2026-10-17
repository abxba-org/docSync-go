package gitpr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func covFixedTime() string       { return "2026-01-01T00:00:00.000Z" }
func covStrPtr(v string) *string { return &v }
func covIntPtr(v int) *int       { return &v }

func covSha() string { return "0123456789abcdef0123456789abcdef01234567" }

// covShortShaUpper is a 40-hex UPPERCASE form (must project to lowercase).
func covShortShaUpper() string { return strings.ToUpper(covSha()) }

/* --------------------------------------------- idempotency (INV-11) */

// TestCOV_GITPR_013_IdempotencyExcerptAndKeys pins the bounded JSON echo and
// the key derivation's full refusal surface (fail closed on malformed input).
func TestCOV_GITPR_013_IdempotencyExcerptAndKeys(t *testing.T) {
	assert.Equal(t, `""`, idemJSONExcerpt(""))
	assert.Equal(t, `"\""`, idemJSONExcerpt(`"`))
	assert.Equal(t, `"\\"`, idemJSONExcerpt(`\`))
	assert.Equal(t, `"\b"`, idemJSONExcerpt("\b"))
	assert.Equal(t, `"\f"`, idemJSONExcerpt("\f"))
	assert.Equal(t, `"\n"`, idemJSONExcerpt("\n"))
	assert.Equal(t, `"\r"`, idemJSONExcerpt("\r"))
	assert.Equal(t, `"\t"`, idemJSONExcerpt("\t"))
	assert.Equal(t, `"\u001f"`, idemJSONExcerpt("\x1f"))
	assert.Equal(t, `"\u0010"`, idemJSONExcerpt("\x10"))
	assert.Equal(t, `"é"`, idemJSONExcerpt("é"))
	// DEL (0x7f) is below no control threshold that matters — it renders raw.
	assert.Equal(t, "\"\u007f\"", idemJSONExcerpt("\u007f"))

	// idemFirstRunes64: over-64 input is truncated, exactly-64 passes through.
	assert.Equal(t, "abc", idemFirstRunes64("abc"))
	var long strings.Builder
	for i := 0; i < 100; i++ {
		long.WriteRune('é')
	}
	assert.Len(t, []rune(idemFirstRunes64(long.String())), 64)
	var exact strings.Builder
	for i := 0; i < 64; i++ {
		exact.WriteRune('a')
	}
	assert.Len(t, idemFirstRunes64(exact.String()), 64)

	// idemCanonicalJSON: an unserializable value fails (fail closed at hashing).
	_, err := idemCanonicalJSON(map[string]any{"bad": make(chan int)})
	require.Error(t, err)

	// DocSetContentHash: order-independent over patch permutations.
	left := schemas.DocPatchExample
	left.DocPath = "docs/b.md"
	right := schemas.DocPatchExample
	right.DocPath = "docs/a.md"
	forward, err := DocSetContentHash([]schemas.DocPatch{left, right})
	require.NoError(t, err)
	require.NotEmpty(t, forward)
	backward, err := DocSetContentHash([]schemas.DocPatch{right, left})
	require.NoError(t, err)
	assert.Equal(t, forward, backward, "the doc-set hash must be order-independent")

	empty, err := DocSetContentHash(nil)
	require.NoError(t, err)
	assert.NotEmpty(t, empty)

	// ComputeIdempotencyKey: all four forms plus every refusal.
	sha := covSha()
	sourceForm, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		SourcePrHeadSha: &sha, DocSetContentHash: forward,
	})
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(sha)+":"+forward, sourceForm)

	localForm, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{DocSetContentHash: forward})
	require.NoError(t, err)
	assert.Equal(t, LocalKeyHead+":"+forward, localForm)

	localRunForm, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		DocSetContentHash: forward, RunID: "run_01J",
	})
	require.NoError(t, err)
	assert.Equal(t, LocalKeyHead+":run_01J:"+forward, localRunForm)

	upper := covShortShaUpper()
	upperForm, err := ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		SourcePrHeadSha: &upper, DocSetContentHash: forward,
	})
	require.NoError(t, err)
	assert.Equal(t, sourceForm, upperForm, "an uppercase sha normalizes")

	_, err = ComputeIdempotencyKey(ComputeIdempotencyKeyInput{DocSetContentHash: ""})
	require.Error(t, err)
	assert.Equal(t, CodeIdempotencyKey, docutil.CodeOf(err))

	_, err = ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		SourcePrHeadSha: covStrPtr("nope"), DocSetContentHash: forward,
	})
	require.Error(t, err)

	_, err = ComputeIdempotencyKey(ComputeIdempotencyKeyInput{
		DocSetContentHash: forward, RunID: "bad run/id",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runId must be a safe branch fragment")
	assert.LessOrEqual(t, len(err.Error()), 400)
}

/* ------------------------------------------ merge ledger (P6-T18 / R15) */

// TestCOV_GITPR_014_MergeLedgerSurface pins the ledger's construction, its
// load/record refusals, the legal transition matrix and the CAS-conflict
// projection (R15: the loser never overwrites the winner).
func TestCOV_GITPR_014_MergeLedgerSurface(t *testing.T) {
	_, err := NewMergeLedgerStore(MergeLedgerStoreDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deps.store")

	store := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	ledger, err := NewMergeLedgerStore(MergeLedgerStoreDeps{Store: store})
	require.NoError(t, err)

	// Load: an empty key is refused; a missing ledger is a soft nil.
	_, err = ledger.Load("")
	require.Error(t, err)
	missing, err := ledger.Load("pr-999")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// Record: the empty-key and nil-clock refusals.
	_, err = ledger.Record("", RecordLedgerEntryInput{}, covFixedTime, nil)
	require.Error(t, err)
	_, err = ledger.Record("pr-1", RecordLedgerEntryInput{}, nil, nil)
	require.Error(t, err)

	// An invalid entry shape is refused (fail closed, nothing written).
	_, err = ledger.Record("pr-1", RecordLedgerEntryInput{
		DocPRNumber: 0, State: schemas.MergeLedgerStateOpen, IdempotencyKey: "k", Detail: "d",
	}, covFixedTime, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ledger entry fails")

	// A good record lands; the same PR number UPSERTS in place.
	first, err := ledger.Record("pr-1", RecordLedgerEntryInput{
		DocPRNumber: 1, State: schemas.MergeLedgerStateOpen, IdempotencyKey: "k1", Detail: "first",
	}, covFixedTime, nil)
	require.NoError(t, err)
	assert.True(t, first.OK)
	require.NotEmpty(t, first.Hash)

	second, err := ledger.Record("pr-1", RecordLedgerEntryInput{
		DocPRNumber: 1, State: schemas.MergeLedgerStateMerged, IdempotencyKey: "k1", Detail: "second",
	}, covFixedTime, nil)
	require.NoError(t, err)
	assert.True(t, second.OK)

	loaded, err := ledger.Load("pr-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Len(t, loaded.Entries, 1)
	assert.Equal(t, schemas.MergeLedgerStateMerged, loaded.Entries[0].State)

	// A stale expected hash is a CAS conflict (OK false), never an overwrite.
	stale := strings.Repeat("0", 64)
	conflicted, err := ledger.Record("pr-1", RecordLedgerEntryInput{
		DocPRNumber: 2, State: schemas.MergeLedgerStateOpen, IdempotencyKey: "k2", Detail: "conflict",
	}, covFixedTime, &RecordLedgerOptions{ExpectedHash: &stale})
	require.NoError(t, err)
	assert.False(t, conflicted.OK)
	assert.Contains(t, conflicted.Detail, "CAS conflict")
	assert.Contains(t, conflicted.Detail, "a concurrent writer won")
	assert.Contains(t, conflicted.Detail, "never overwrite the winner")

	reloaded, err := ledger.Load("pr-1")
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	require.Len(t, reloaded.Entries, 1, "the winner's entry survives")

	// Transition matrix: legal moves, illegal moves, unknown and terminal states.
	next, err := LedgerTransition(schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateSuperseded)
	require.NoError(t, err)
	assert.Equal(t, schemas.MergeLedgerStateSuperseded, next)
	_, err = LedgerTransition(schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "illegal ledger transition")
	// A legal-but-non-first target still resolves, and 'dropped' is terminal.
	_, err = LedgerTransition(schemas.MergeLedgerStateOpen, schemas.MergeLedgerStateConflicted)
	require.NoError(t, err)
	_, err = LedgerTransition(schemas.MergeLedgerStateMerged, schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	_, err = LedgerTransition(schemas.MergeLedgerState("bogus"), schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown current ledger state")
	_, err = LedgerTransition(schemas.MergeLedgerStateDropped, schemas.MergeLedgerStateOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is terminal")
}

/* ------------------------------------------- incident log (INV-9 / OD-R50) */

// TestCOV_GITPR_015_IncidentLogSurface pins the sink-builder refusal, the
// canonical-line renderers and the Record path's fail-closed validation plus
// its single-retry sink failure.
func TestCOV_GITPR_015_IncidentLogSurface(t *testing.T) {
	_, err := FileIncidentSink("relative/path")
	require.Error(t, err)
	var sinkErr *IncidentSinkError
	require.ErrorAs(t, err, &sinkErr)
	assert.Contains(t, err.Error(), "stateDir must be an absolute path")

	dir := t.TempDir()
	sink, err := FileIncidentSink(dir)
	require.NoError(t, err)
	log := CreateIncidentLog(sink)

	first, err := log.Record(context.Background(), RecordIncidentInput{
		At: covFixedTime(), RunID: "run_1",
		Rule: schemas.SafetyIncidentRuleProtectedPathPatch, OffendingPath: "manifest.yml",
		Detail: "touched a protected path", DocPath: covStrPtr("docs/a.md"), SourcePRNumber: covIntPtr(481),
	})
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.True(t, strings.HasPrefix(first.IncidentID, "inc-"))
	assert.Len(t, first.IncidentID, 4+16)

	raw, err := os.ReadFile(filepath.Join(dir, IncidentLogFilename))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], `"incident_id"`)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &decoded))
	assert.Equal(t, first.IncidentID, decoded["incident_id"])
	assert.Equal(t, "manifest.yml", decoded["offending_path"])
	assert.Equal(t, float64(481), decoded["source_pr_number"])
	assert.Equal(t, "docs/a.md", decoded["doc_path"])
	assert.Equal(t, "2026-01-01T00:00:00.000Z", decoded["at"])
	// Canonical JSON sorts keys: "at" precedes "detail" precedes "rule".
	assert.Less(t, strings.Index(lines[0], `"at"`), strings.Index(lines[0], `"rule"`))

	// The standalone RecordIncident form is the same call.
	log2 := CreateIncidentLog(sink)
	second, err := RecordIncident(context.Background(), log2, RecordIncidentInput{
		At: covFixedTime(), RunID: "run_1",
		Rule: schemas.SafetyIncidentRuleOutOfAllowlistPath, OffendingPath: "src/main.go",
		Detail: "outside the manifest allowlist",
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.IncidentID, second.IncidentID)

	raw, err = os.ReadFile(filepath.Join(dir, IncidentLogFilename))
	require.NoError(t, err)
	lines = strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 2, "the sink appends; it never truncates")

	// The bare helpers: JSON rendering, truncation, and the issue summary.
	assert.Equal(t, `"plain"`, incidentJSONString("plain"))
	assert.Equal(t, `"tab\there"`, incidentJSONString("tab\there"))
	assert.Len(t, incidentTruncate(strings.Repeat("x", 200), 64), 64)
	assert.Equal(t, "abc", incidentTruncate("abc", 64))
	assert.Contains(t, incidentIssueSummary(assert.AnError), "assert.AnError")

	// The nil DocPath/SourcePRNumber branch of compose keeps both fields off.
	withoutDoc, err := log.Record(context.Background(), RecordIncidentInput{
		At: covFixedTime(), RunID: "run_1",
		Rule: schemas.SafetyIncidentRuleProtectedPathPatch, OffendingPath: "manifest.yml",
		Detail: "no doc path",
	})
	require.NoError(t, err)
	assert.Nil(t, withoutDoc.DocPath)
	assert.Nil(t, withoutDoc.SourcePRNumber)

	// A sink that always fails: exactly one retry, then a loud wrapping error.
	failing := &covFailingSink{}
	_, err = CreateIncidentLog(failing).Record(context.Background(), RecordIncidentInput{
		At: covFixedTime(), RunID: "run_2",
		Rule: schemas.SafetyIncidentRuleProtectedPathPatch, OffendingPath: "manifest.yml",
		Detail: "sink fails",
	})
	require.Error(t, err)
	var wrapErr *IncidentSinkError
	require.ErrorAs(t, err, &wrapErr)
	assert.Contains(t, err.Error(), "after one retry")
	assert.Equal(t, 2, failing.calls)

	// A sink failing once then succeeding is retried successfully (2 calls).
	flaky := &covFlakySink{failures: 1}
	ok, err := CreateIncidentLog(flaky).Record(context.Background(), RecordIncidentInput{
		At: covFixedTime(), RunID: "run_2",
		Rule: schemas.SafetyIncidentRuleProtectedPathPatch, OffendingPath: "manifest.yml",
		Detail: "flaky sink",
	})
	require.NoError(t, err)
	require.NotNil(t, ok)
	assert.Equal(t, 2, flaky.calls)

	// A malformed record is refused before ANY write (fail closed).
	counting := &covCountingSink{}
	_, err = CreateIncidentLog(counting).Record(context.Background(), RecordIncidentInput{
		At: covFixedTime(), RunID: "run_3",
		Rule: schemas.SafetyIncidentRule("bogus-rule"), OffendingPath: "manifest.yml",
		Detail: "invalid rule",
	})
	require.Error(t, err)
	var validationErr *IncidentValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, 0, counting.calls, "an invalid incident is never written")
}

type covFailingSink struct{ calls int }

func (s *covFailingSink) Append(_ context.Context, _ *schemas.SafetyIncident) error {
	s.calls++
	return assert.AnError
}

type covFlakySink struct {
	calls    int
	failures int
}

func (s *covFlakySink) Append(_ context.Context, _ *schemas.SafetyIncident) error {
	s.calls++
	if s.calls <= s.failures {
		return assert.AnError
	}
	return nil
}

type covCountingSink struct{ calls int }

func (s *covCountingSink) Append(_ context.Context, _ *schemas.SafetyIncident) error {
	s.calls++
	return nil
}

/* ---------------------------------------- feedback capture (T21/T23/T24) */

// TestCOV_GITPR_016_FeedbackCaptureSurface pins every reviewer-event arm of the
// normalization table (never 'accepted' without a merge), the false-negative
// emission and the proposal queue's nil-receiver edge.
func TestCOV_GITPR_016_FeedbackCaptureSurface(t *testing.T) {
	action := NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventMerged})
	assert.Equal(t, schemas.FeedbackSignalAccepted, action.Signal)
	assert.Contains(t, action.Detail, "merged")

	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventReviewChanges, EditedDiff: "@@ -1 +1 @@"})
	assert.Equal(t, schemas.FeedbackSignalCorrected, action.Signal)
	require.NotNil(t, action.CorrectedDiff)
	require.NotNil(t, action.Basis)
	assert.Equal(t, schemas.FeedbackBasisReviewEvent, *action.Basis)

	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventReviewChanges})
	assert.Equal(t, schemas.FeedbackSignalCorrected, action.Signal)
	require.NotNil(t, action.Basis)
	assert.Equal(t, schemas.FeedbackBasisPRHead, *action.Basis)
	assert.Nil(t, action.CorrectedDiff)
	assert.Contains(t, action.Detail, "fails closed at parse time")

	action = NormalizeReviewerAction(ReviewerEvent{
		Kind: ReviewerEventClosedRejected, StatedReason: "docs are already correct now",
	})
	assert.Equal(t, schemas.FeedbackSignalFalsePositive, action.Signal)
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventClosedRejected, StatedReason: "style nit"})
	assert.Equal(t, schemas.FeedbackSignalStyleRejected, action.Signal)
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventClosedRejected})
	assert.Equal(t, schemas.FeedbackSignalStyleRejected, action.Signal)
	assert.Contains(t, action.Detail, "<none>")

	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventLabel, Label: "docsync:intentional"})
	assert.Equal(t, schemas.FeedbackSignalOverrideAck, action.Signal)
	assert.Contains(t, action.Detail, "operator override")
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventLabel})
	assert.Equal(t, schemas.FeedbackSignalUnlabeled, action.Signal)
	assert.Contains(t, action.Detail, "no label provided")
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventLabel, Label: "wontfix"})
	assert.Equal(t, schemas.FeedbackSignalUnlabeled, action.Signal)
	assert.Contains(t, action.Detail, "not a docsync override label")

	for _, kind := range []ReviewerEventKind{ReviewerEventTimeout, ReviewerEventReviewComment} {
		action = NormalizeReviewerAction(ReviewerEvent{Kind: kind})
		assert.Equal(t, schemas.FeedbackSignalUnlabeled, action.Signal)
	}
	// An unknown kind fails closed (never accepted).
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventKind("future-kind")})
	assert.Equal(t, schemas.FeedbackSignalUnlabeled, action.Signal)
	assert.Contains(t, action.Detail, "fail closed")

	action = NormalizeReviewerAction(ReviewerEvent{
		Kind: ReviewerEventFalseNegative, MissedDocs: []string{"docs/a.md", "docs/b.md"},
	})
	assert.Equal(t, schemas.FeedbackSignalFalseNegative, action.Signal)
	assert.Contains(t, action.Detail, "docs/a.md")
	action = NormalizeReviewerAction(ReviewerEvent{Kind: ReviewerEventFalseNegative})
	assert.Equal(t, schemas.FeedbackSignalFalseNegative, action.Signal)
	assert.Contains(t, action.Detail, "none recorded")

	assert.Equal(t, `"x"`, feedbackJSONString("x"))
	assert.Equal(t, `"a\nb"`, feedbackJSONString("a\nb"))

	record, err := EmitFeedbackRecord(FeedbackCaptureInput{
		Event: ReviewerEvent{Kind: ReviewerEventMerged},
		RunID: "run_fb", PolicyVersion: 17,
		SourcePrNumber: covIntPtr(481), DocPrNumber: covIntPtr(902),
		DocPath: covStrPtr("docs/a.md"), DecisionID: covStrPtr("d1"), Clock: covFixedTime,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.FeedbackSourceDocPR, record.Source)
	assert.True(t, strings.HasPrefix(record.RecordID, "fb-"))
	assert.Equal(t, covFixedTime(), record.At)

	// The doc-pr number flips the source to 'doc_pr'; absent means 'source_pr'.
	sourceRecord, err := EmitFeedbackRecord(FeedbackCaptureInput{
		Event: ReviewerEvent{Kind: ReviewerEventTimeout},
		RunID: "run_fb2", PolicyVersion: 17,
		SourcePrNumber: covIntPtr(482), DecisionID: covStrPtr("d2"), Clock: covFixedTime,
	})
	require.NoError(t, err)
	assert.Equal(t, schemas.FeedbackSourceSourcePR, sourceRecord.Source)
	assert.Nil(t, sourceRecord.DocPRNumber)

	report := DetectFalseNegative(RoutingIgnore, []string{"src/a.ts", "docs/a.md"}, []string{"docs/a.md"})
	assert.True(t, report.FalseNegative)
	assert.Equal(t, []string{"docs/a.md"}, report.TouchedDocs)
	assert.False(t, DetectFalseNegative(RoutingContinue, []string{"docs/a.md"}, []string{"docs/a.md"}).FalseNegative)
	assert.False(t, DetectFalseNegative(RoutingReview, []string{"docs/a.md"}, []string{"docs/a.md"}).FalseNegative)

	// Detection with nothing to report emits nothing.
	emission, err := EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingContinue, SourcePrDiffPaths: []string{"docs/a.md"},
		ManifestDocPaths: []string{"docs/a.md"}, RunID: "run_fn",
		PolicyVersion: 17, DecisionID: covStrPtr("d1"), Clock: covFixedTime,
	})
	require.NoError(t, err)
	assert.Nil(t, emission.Record)

	emission, err = EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingIgnore, SourcePrDiffPaths: []string{"docs/b.md", "docs/a.md"},
		ManifestDocPaths: []string{"docs/a.md", "docs/b.md"}, RunID: "run_fn",
		PolicyVersion: 17, DecisionID: covStrPtr("d1"),
		SourcePrNumber: covIntPtr(7), Clock: covFixedTime,
	})
	require.NoError(t, err)
	require.NotNil(t, emission.Record)
	assert.Equal(t, schemas.FeedbackSignalFalseNegative, emission.Record.Signal)
	require.NotNil(t, emission.Record.DocPath)
	assert.Equal(t, "docs/a.md", *emission.Record.DocPath)
	assert.Contains(t, emission.Record.Detail, "docs/a.md, docs/b.md")

	chosen := "docs/b.md"
	emission, err = EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingIgnore, SourcePrDiffPaths: []string{"docs/a.md"},
		ManifestDocPaths: []string{"docs/a.md"}, RunID: "run_fn",
		PolicyVersion: 17, DecisionID: covStrPtr("d1"), DocPath: &chosen, Clock: covFixedTime,
	})
	require.NoError(t, err)
	require.NotNil(t, emission.Record)
	assert.Equal(t, "docs/b.md", *emission.Record.DocPath)

	// A missing decision_id fails closed on the rejection signal.
	_, err = EmitFalseNegativeRecord(FalseNegativeEmissionInput{
		Routing: RoutingIgnore, SourcePrDiffPaths: []string{"docs/a.md"},
		ManifestDocPaths: []string{"docs/a.md"}, RunID: "run_fn",
		PolicyVersion: 17, Clock: covFixedTime,
	})
	require.Error(t, err)

	// The proposal queue: nil receivers are silent no-ops (never a panic).
	var nilQueue *ProposalQueue
	nilQueue.Push(ProposalEntry{})
	assert.Empty(t, nilQueue.Drain())

	queue := CreateProposalQueue()
	queue.Push(ProposalEntry{
		Kind: ProposalEntryKind, Pattern: "docs/x/**", PolicyVersion: 17,
		At: covFixedTime(), RunID: "run_fb",
	})
	drained := queue.Drain()
	require.Len(t, drained, 1)
	assert.Equal(t, "docs/x/**", drained[0].Pattern)
	assert.Equal(t, drained, queue.Drain(), "Drain is non-destructive: it snapshots")
}

/* --------------------------------------------- check runs (Phase 8) */

// covRecordingTransport answers keys by request identity (no positional FIFO).
type covRecordingTransport struct {
	responses map[string]string
	seen      []string
}

func (t *covRecordingTransport) send(request GitHubRestRequest) (GitHubRestResponse, error) {
	path := request.URL
	if idx := strings.Index(path, "://"); idx >= 0 {
		if slash := strings.Index(path[idx+3:], "/"); slash >= 0 {
			path = path[idx+3+slash:]
		}
	}
	if q := strings.Index(path, "?"); q >= 0 {
		path = path[:q]
	}
	key := request.Method + " " + path
	t.seen = append(t.seen, key)
	body, ok := t.responses[key]
	if !ok {
		return GitHubRestResponse{}, assert.AnError
	}
	return GitHubRestResponse{
		Status:  200,
		Headers: map[string]string{"content-type": "application/json"},
		Body:    body,
	}, nil
}

// TestCOV_GITPR_017_CheckRunSurface pins the check-run client refusals, the
// response projections and the bounded-evidence rule on error details.
func TestCOV_GITPR_017_CheckRunSurface(t *testing.T) {
	// A nil transport defaults to the fetch transport (never nil).
	client := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
		Clock:       covFixedTime,
	})
	require.NotNil(t, client)

	transport := &covRecordingTransport{responses: map[string]string{
		"POST /repos/acme/widgets/check-runs":                         `{"id": 5, "html_url": "https://x"}`,
		"PATCH /repos/acme/widgets/check-runs/5":                      `{"id": 5, "status": "completed"}`,
		"GET /repos/acme/widgets/commits/" + covSha() + "/check-runs": `{"check_runs": [{"id": 5, "name": "docsync/check"}]}`,
	}}
	seamed := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "t"},
		Transport:   transport.send,
		Clock:       covFixedTime,
	})

	// repoPath refusals on every write op.
	_, err := seamed.CreateCheckRun(context.Background(), "", CreateCheckRunInput{
		HeadSha: covSha(), Status: CheckRunStatusQueued,
	})
	require.Error(t, err)
	_, err = seamed.UpdateCheckRun(context.Background(), "noslash", 3, UpdateCheckRunInput{})
	require.Error(t, err)

	// kind:'none' fails closed on both write operations (INV-8).
	noneClient := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsNone},
		Transport:   transport.send,
		Clock:       covFixedTime,
	})
	_, err = noneClient.CreateCheckRun(context.Background(), "acme/widgets", CreateCheckRunInput{
		HeadSha: covSha(), Status: CheckRunStatusQueued,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")
	_, err = noneClient.UpdateCheckRun(context.Background(), "acme/widgets", 3, UpdateCheckRunInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials configured")

	// A non-positive check-run id is refused on update.
	_, err = seamed.UpdateCheckRun(context.Background(), "acme/widgets", 0, UpdateCheckRunInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed check-run id argument")

	// A real create/update/find round trip.
	created, err := seamed.CreateCheckRun(context.Background(), "acme/widgets", CreateCheckRunInput{
		HeadSha: covSha(), Status: CheckRunStatusQueued, StartedAt: covStrPtr(covFixedTime()),
	})
	require.NoError(t, err)
	assert.Equal(t, 5, created.ID)

	status := CheckRunStatusCompleted
	conclusion := CheckRunConclusionSuccess
	updated, err := seamed.UpdateCheckRun(context.Background(), "acme/widgets", 5, UpdateCheckRunInput{
		Status: &status, Conclusion: &conclusion, CompletedAt: covStrPtr(covFixedTime()),
	})
	require.NoError(t, err)
	assert.Equal(t, 5, updated.ID)

	found, err := seamed.FindCheckRunsForRef(context.Background(), "acme/widgets", covSha())
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, 5, found[0].ID)

	// A non-check-run body fails closed — and NEVER echoes the token.
	badClient := CreateCheckRunClient(CheckRunClientDeps{
		Credentials: PrCredentials{Kind: PrCredentialsDeveloper, Token: "secret-token"},
		Transport: (&covRecordingTransport{responses: map[string]string{
			"GET /repos/acme/widgets/commits/abc/check-runs":              `{"unexpected": true}`,
			"GET /repos/acme/widgets/commits/" + covSha() + "/check-runs": `{"unexpected": true}`,
		}}).send,
		Clock: covFixedTime,
	})
	_, err = badClient.FindCheckRunsForRef(context.Background(), "acme/widgets", "abc")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token", "the token never survives into a detail")

	// parseCheckRun projections: null/text fields and malformed bodies.
	run, err := (&checkRunClient{}).parseCheckRun(GitHubRestResponse{
		Status: 200, Body: `{"id":9,"name":"docsync/check","conclusion":null,"html_url":"https://x"}`,
	})
	require.NoError(t, err)
	assert.Equal(t, 9, run.ID)
	assert.Nil(t, run.Conclusion)
	require.NotNil(t, run.Name)
	assert.Equal(t, "docsync/check", *run.Name)
	_, err = (&checkRunClient{}).parseCheckRun(GitHubRestResponse{Status: 200, Body: `{"id":"x"}`})
	require.Error(t, err)
	_, err = (&checkRunClient{}).parseCheckRun(GitHubRestResponse{Status: 200, Body: `[]`})
	require.Error(t, err)

	// marshalCheckRunBody renders and fails closed on the unserializable case.
	rendered, err := marshalCheckRunBody(map[string]any{"status": "queued"})
	require.NoError(t, err)
	assert.Contains(t, *rendered, "queued")
	_, err = marshalCheckRunBody(map[string]any{"bad": make(chan int)})
	require.Error(t, err)
	assert.Len(t, checkRunBodyExcerpt(strings.Repeat("y", 600)), 500)
}

/* ------------------------------------------------------- client retry */

// TestCOV_GITPR_018_ClientRetryAndStatus pins the structural status probe and
// the WithGoneRetry composition (non-410 passthrough, exhaustion, retries<0).
func TestCOV_GITPR_018_ClientRetryAndStatus(t *testing.T) {
	err := NewGitHubRestError("boom", GitHubRestErrorInit{Detail: "d", Status: covIntPtr(410)})
	status, ok := structuralStatus(err)
	assert.True(t, ok)
	assert.Equal(t, 410, status)
	plain, ok := structuralStatus(assert.AnError)
	assert.False(t, ok)
	assert.Equal(t, 0, plain)
	_, ok = structuralStatus(nil)
	assert.False(t, ok)

	// A non-410 passthrough propagates immediately.
	calls := 0
	_, passthrough := WithGoneRetry(func() (int, error) {
		calls++
		return 0, assert.AnError
	}, &GoneRetryOptions{Delay: func(time.Duration) {}})
	require.ErrorIs(t, passthrough, assert.AnError)
	assert.Equal(t, 1, calls)

	// A 410 error is retried once, then surfaced on exhaustion.
	goneErr := NewGitHubRestError("gone", GitHubRestErrorInit{Detail: "gone", Status: covIntPtr(410)})
	calls = 0
	waited := 0
	_, afterRetry := WithGoneRetry(func() (int, error) {
		calls++
		return 0, goneErr
	}, &GoneRetryOptions{Retries: covIntPtr(1), Delay: func(time.Duration) { waited++ }})
	require.ErrorIs(t, afterRetry, goneErr)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, waited)

	// Zero retries ⇒ exactly one attempt.
	calls = 0
	_, afterZero := WithGoneRetry(func() (int, error) {
		calls++
		return 0, goneErr
	}, &GoneRetryOptions{Retries: covIntPtr(0)})
	require.Error(t, afterZero)
	assert.Equal(t, 1, calls)

	// A negative retry budget fails closed.
	_, negative := WithGoneRetry(func() (int, error) { return 1, nil }, &GoneRetryOptions{Retries: covIntPtr(-1)})
	require.Error(t, negative)
	assert.Equal(t, docutil.CodeInternal, docutil.CodeOf(negative))

	// Nil options use the defaults (a real sleep is injected).
	defaultedVal, defaultedErr := WithGoneRetry(func() (int, error) { return 7, nil }, nil)
	require.NoError(t, defaultedErr)
	assert.Equal(t, 7, defaultedVal)
}

/* -------------------------------------------------------------- backoff */

// TestCOV_GITPR_019_BackoffRetries pins the rate-limit retry helper: a
// non-429/403 failure passes through immediately, exhaustion yields the
// ok:false report-only outcome, and the attempt number is 1-based.
func TestCOV_GITPR_019_BackoffRetries(t *testing.T) {
	waits := 0
	backoff, err := CreateBackoff(BackoffConfig{
		MaxAttempts: 3, BaseDelayMs: 1, Rng: func() float64 { return 0 },
	}, func(float64) { waits++ })
	require.NoError(t, err)

	attempts := 0
	outcome, err := RetryWithBackoff(backoff, func(attempt int) (int, error) {
		attempts++
		assert.Equal(t, attempts, attempt) // 1-based
		if attempts < 3 {
			return 0, NewGitHubRateLimitError("rate limited", GitHubRateLimitErrorInit{Status: covIntPtr(429)})
		}
		return 42, nil
	})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Equal(t, 42, outcome.Value)
	assert.Equal(t, 3, outcome.Attempts)
	assert.Equal(t, 2, waits)

	// Exhaustion: ok:false, the LAST rate-limit status, and no final delay.
	waits = 0
	outcome, err = RetryWithBackoff(backoff, func(attempt int) (int, error) {
		return 0, NewGitHubRateLimitError("rate limited", GitHubRateLimitErrorInit{Status: covIntPtr(429)})
	})
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.Equal(t, 3, outcome.Attempts)
	require.NotNil(t, outcome.LastStatus)
	assert.Equal(t, 429, *outcome.LastStatus)
	assert.Equal(t, 2, waits, "the backoff never delays after the final attempt")

	// A non-retryable error propagates immediately, unmodified.
	attempts = 0
	_, err = RetryWithBackoff(backoff, func(int) (int, error) {
		attempts++
		return 0, assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, attempts)

	// A 403 is also retried (the secondary-rate family) until success.
	waits = 0
	attempts = 0
	forbidden := NewGitHubRateLimitError("secondary rate", GitHubRateLimitErrorInit{Status: covIntPtr(403)})
	outcome, err = RetryWithBackoff(backoff, func(int) (int, error) {
		attempts++
		if attempts < 2 {
			return 0, forbidden
		}
		return 5, nil
	})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Equal(t, 5, outcome.Value)

	// CreateBackoff validates the config surface (fail closed).
	_, err = CreateBackoff(BackoffConfig{MaxAttempts: 0})
	require.Error(t, err)
	assert.Equal(t, CodeBackoffConfig, docutil.CodeOf(err))
	_, err = CreateBackoff(BackoffConfig{MaxAttempts: 3, BaseDelayMs: -1})
	require.Error(t, err)
	assert.Equal(t, CodeBackoffConfig, docutil.CodeOf(err))

	// A nil Rng falls back to the deterministic attempt-seeded LCG.
	seeded, err := CreateBackoff(BackoffConfig{MaxAttempts: 2, BaseDelayMs: 1}, func(float64) {})
	require.NoError(t, err)
	require.NotNil(t, seeded)
	assert.GreaterOrEqual(t, lcgJitter(1), 0.0)
	assert.LessOrEqual(t, lcgJitter(1), 1.0)
	assert.Contains(t, backoffNumberText(10), "10")

	// json is exercised for the response-projections above.
	_ = json.Marshal
}
