package gitpr_test

// Black-box port of the incident-log half of test/gitpr-supersede.test.ts
// (P6-T12; PLAN §7.3 "PR-side safety"; ARCH §11.4).
//
// The append-only safety-incident log: schema-validated on every write (fail
// closed — an invalid record is never persisted), deterministic `inc-<sha16>`
// ids derived from the canonical JSON of the record without the id, a file sink
// that only ever APPENDS to `<stateDir>/incidents.jsonl`, and fail-loud sink
// failures (exactly one retry, then a typed IncidentSinkError carrying the
// retried attempt as its cause).
//
// No network, no wall clock (P19) — the file sinks use temp dirs and the clock
// is a constant.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

const (
	incidentBaseAt    = "2026-10-07T00:00:00.000Z"
	incidentRunID     = "run_01J9ZK6X8A"
	incidentSchemaTag = "docsync.safety-incident/v1"
	incidentIDPattern = `^inc-[0-9a-f]{16}$`
)

func incidentBaseInput() gitpr.RecordIncidentInput {
	return gitpr.RecordIncidentInput{
		At:             incidentBaseAt,
		RunID:          incidentRunID,
		Rule:           schemas.SafetyIncidentRuleOutOfAllowlistPath,
		OffendingPath:  "src/handlers/payments.ts",
		Detail:         "patch attempt touched a path outside the manifest allowlist — blocked, fail-closed",
		DocPath:        incidentStrPtr("docs/payments.md"),
		SourcePRNumber: incidentIntPtr(42),
	}
}

func incidentStrPtr(s string) *string { return &s }
func incidentIntPtr(i int) *int       { return &i }

func incidentTempStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state")
}

func incidentReadLog(t *testing.T, stateDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, gitpr.IncidentLogFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
	}
	text := string(raw)
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}

// incidentCountingSink is the TS CountingSink double: it fails the first
// `failTimes` appends, then succeeds.
type incidentCountingSink struct {
	mu        sync.Mutex
	calls     int
	failTimes int
	recorded  []*schemas.SafetyIncident
}

func (s *incidentCountingSink) Append(_ context.Context, incident *schemas.SafetyIncident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failTimes {
		return errIncidentDiskOnFire
	}
	s.recorded = append(s.recorded, incident)
	return nil
}

var errIncidentDiskOnFire = &incidentStubError{msg: "disk on fire"}

type incidentStubError struct{ msg string }

func (e *incidentStubError) Error() string { return e.msg }

func incidentFileSink(t *testing.T, stateDir string) gitpr.IncidentSink {
	t.Helper()
	sink, err := gitpr.FileIncidentSink(stateDir)
	require.NoError(t, err)
	return sink
}

func TestP6T12_IncidentsAppendOnlyAndSchemaValidated(t *testing.T) {
	stateDir := incidentTempStateDir(t)
	log := gitpr.CreateIncidentLog(incidentFileSink(t, stateDir))

	first, err := log.Record(context.Background(), incidentBaseInput())
	require.NoError(t, err)
	second, err := log.Record(context.Background(), gitpr.RecordIncidentInput{
		At:            "2026-10-07T00:00:01.000Z",
		RunID:         incidentRunID,
		Rule:          schemas.SafetyIncidentRuleProtectedPathPatch,
		OffendingPath: "manifest.yml",
		Detail:        "patch attempt targeted a protected path",
	})
	require.NoError(t, err)

	lines := incidentReadLog(t, stateDir)
	require.Len(t, lines, 2, "two incidents → exactly 2 lines")

	parsed := make([]*schemas.SafetyIncident, 0, len(lines))
	for _, line := range lines {
		incident, err := schemas.ParseSafetyIncident([]byte(line))
		require.NoError(t, err, "every persisted line is a schema-valid incident")
		parsed = append(parsed, incident)
	}
	require.Equal(t, first.IncidentID, parsed[0].IncidentID)
	require.Equal(t, second.IncidentID, parsed[1].IncidentID)
	require.Regexp(t, regexp.MustCompile(incidentIDPattern), parsed[0].IncidentID)
	require.Equal(t, incidentBaseAt, parsed[0].At)
	require.Equal(t, incidentRunID, parsed[0].RunID)
	require.Equal(t, schemas.SafetyIncidentRuleOutOfAllowlistPath, parsed[0].Rule)
	require.Equal(t, "src/handlers/payments.ts", parsed[0].OffendingPath)
	require.Equal(t, "docs/payments.md", *parsed[0].DocPath)
	require.Equal(t, 42, *parsed[0].SourcePRNumber)
	require.Equal(t, schemas.SafetyIncidentRuleProtectedPathPatch, parsed[1].Rule)
	require.Nil(t, parsed[1].DocPath, "optional keys omitted when absent")
	require.Nil(t, parsed[1].SourcePRNumber)
	require.Contains(t, lines[0], incidentSchemaTag) // one JSON object per line
	require.NotContains(t, strings.TrimRight(lines[0], "\n"), "\n")
	require.Regexp(t, regexp.MustCompile(`"schema":"`+incidentSchemaTag+`"`), lines[0])

	// Deterministic ids: the same input recorded through a DIFFERENT sink/dir
	// yields the SAME incident_id.
	otherDir := incidentTempStateDir(t)
	replayed, err := gitpr.RecordIncident(context.Background(),
		gitpr.CreateIncidentLog(incidentFileSink(t, otherDir)), incidentBaseInput())
	require.NoError(t, err)
	require.Equal(t, first.IncidentID, replayed.IncidentID)

	// Malformed input → typed refusal, NOTHING written, file byte-identical.
	before, err := os.ReadFile(filepath.Join(stateDir, gitpr.IncidentLogFilename))
	require.NoError(t, err)
	_, err = log.Record(context.Background(), gitpr.RecordIncidentInput{
		At: incidentBaseAt, RunID: incidentRunID,
		Rule: schemas.SafetyIncidentRule("not_a_rule"), OffendingPath: "x", Detail: "y",
	})
	require.Error(t, err)
	var invalidErr *gitpr.IncidentValidationError
	require.ErrorAs(t, err, &invalidErr)
	require.Equal(t, gitpr.EDOCSYNCIncidentInvalid, invalidErr.Code())
	require.Contains(t, err.Error(), "safety-incident refused (fail closed, nothing written)")
	require.Contains(t, err.Error(), "rule") // the offending field is named

	_, err = log.Record(context.Background(), gitpr.RecordIncidentInput{
		At: "not-a-date", RunID: incidentRunID,
		Rule: schemas.SafetyIncidentRuleOutOfAllowlistPath, OffendingPath: "x", Detail: "y",
	})
	require.Error(t, err)
	require.ErrorAs(t, err, &invalidErr)

	after, err := os.ReadFile(filepath.Join(stateDir, gitpr.IncidentLogFilename))
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "never rewritten, never truncated")
	require.Len(t, incidentReadLog(t, stateDir), 2)
}

func TestP6T12_IncidentSinkFailureFailsLoudAfterOneRetry(t *testing.T) {
	// Persistently failing sink: exactly ONE retry, then fail LOUD (typed).
	dead := &incidentCountingSink{failTimes: 1 << 30}
	_, err := gitpr.RecordIncident(context.Background(),
		gitpr.CreateIncidentLog(dead), incidentBaseInput())
	require.Error(t, err)
	var sinkErr *gitpr.IncidentSinkError
	require.ErrorAs(t, err, &sinkErr)
	require.Equal(t, gitpr.EDOCSYNCIncidentSink, sinkErr.Code())
	require.Equal(t, 2, dead.calls, "initial attempt + exactly one retry")
	require.ErrorIs(t, err, errIncidentDiskOnFire, "the retried attempt's error is the cause")

	// Flaky sink (fails once, retry succeeds): the incident is recorded.
	flaky := &incidentCountingSink{failTimes: 1}
	saved, err := gitpr.RecordIncident(context.Background(),
		gitpr.CreateIncidentLog(flaky), incidentBaseInput())
	require.NoError(t, err)
	require.Equal(t, 2, flaky.calls)
	require.Regexp(t, regexp.MustCompile(incidentIDPattern), saved.IncidentID)
	require.Len(t, flaky.recorded, 1)
}

func TestP6T12_IncidentLogFileSinkCreatesStateDir(t *testing.T) {
	stateDir := incidentTempStateDir(t)
	log := gitpr.CreateIncidentLog(incidentFileSink(t, stateDir))
	_, err := log.Record(context.Background(), incidentBaseInput())
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(stateDir, gitpr.IncidentLogFilename))
	require.NoError(t, err)
	require.False(t, info.IsDir())

	// A second append EXTENDS the file (append-only, never truncated).
	_, err = log.Record(context.Background(), gitpr.RecordIncidentInput{
		At: "2026-10-07T00:00:02.000Z", RunID: incidentRunID,
		Rule: schemas.SafetyIncidentRuleManifestOrPolicyEdit, OffendingPath: ".docsync/policies/x.yml", Detail: "y",
	})
	require.NoError(t, err)
	require.Len(t, incidentReadLog(t, stateDir), 2)
}

func TestP6T12_FileIncidentSinkRefusesRelativeStateDir(t *testing.T) {
	_, err := gitpr.FileIncidentSink("relative/state")
	require.Error(t, err)
	var sinkErr *gitpr.IncidentSinkError
	require.ErrorAs(t, err, &sinkErr)
	require.Contains(t, err.Error(), "stateDir must be an absolute path")
}

func TestP6T12_EmitPointsRecordTheODR50Rules(t *testing.T) {
	recorder := &incidentCountingSink{}
	log := gitpr.CreateIncidentLog(recorder)

	contained, err := gitpr.EmitInjectionContainedIncident(context.Background(), log, gitpr.IncidentEmitInput{
		At:            incidentBaseAt,
		RunID:         incidentRunID,
		OffendingPath: "docs/readme.md",
		Detail:        "untrusted directive contained at the gateway",
	})
	require.NoError(t, err)
	require.Equal(t, schemas.SafetyIncidentRuleInjectionContained, contained.Rule)

	authority, err := gitpr.EmitAuthorityInfluenceIncident(context.Background(), log, gitpr.IncidentEmitInput{
		At:             incidentBaseAt,
		RunID:          incidentRunID,
		OffendingPath:  ".docsync/policies/promotion.yml",
		Detail:         "model output attempted to rewrite the policy store",
		SourcePRNumber: incidentIntPtr(481),
	})
	require.NoError(t, err)
	require.Equal(t, schemas.SafetyIncidentRuleModelInfluenceOnAuthority, authority.Rule)
	require.NotNil(t, authority.SourcePRNumber)
	require.Equal(t, 481, *authority.SourcePRNumber)
	require.Len(t, recorder.recorded, 2)
}

func TestP6T12_IncidentLogDeterministicIDsDistinctPerContent(t *testing.T) {
	// Same body (same clock) → same id; a different offending path → a
	// different id (content addressing, no collisions across the closed rule
	// vocabulary).
	seen := map[string]string{}
	for _, rule := range schemas.SafetyIncidentRuleValues {
		input := incidentBaseInput()
		input.Rule = rule
		recorder := &incidentCountingSink{}
		recorded, err := gitpr.RecordIncident(context.Background(),
			gitpr.CreateIncidentLog(recorder), input)
		require.NoError(t, err)
		require.Regexp(t, regexp.MustCompile(incidentIDPattern), recorded.IncidentID)
		if other, dup := seen[recorded.IncidentID]; dup {
			t.Fatalf("incident_id collision between %q and %q", other, rule)
		}
		seen[recorded.IncidentID] = string(rule)
	}
}
