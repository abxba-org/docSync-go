package schemas

import (
	"strings"
	"time"
)

// Ported from packages/schemas/src/v1/run-record.ts (RUN_RECORD_SCHEMA +
// RUN_STAGE_SCHEMA + RUN_KEY_SCHEMA + STATE_CHURN_SCHEMA + RUN_RECORD_EXAMPLE).
//
// Run records (ARCH §3.5/§6.3). Idempotency key is
// `(repo, base_sha, head_sha, manifest_version, policy_version)`; re-runs
// upsert, never duplicate. A8 state-churn counters ride here (OD-3, P0-T15)
// so the committed-`state/` revisit is data-driven.
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules). This file also hosts the shared
// datetime/enum-message helpers the sibling schemas ports reuse
// (isRFC3339UTCDatetime, datetimeIssue, optionalDatetimeIssue, stringsOf).

// RunStageStatus is the closed stage-status vocabulary of
// `docsync.run/v1` (RUN_STAGE_STATUS).
type RunStageStatus string

// RunStageStatusValues lists all stage statuses in canonical order
// (RUN_STAGE_STATUS_VALUES).
const (
	RunStageOk           RunStageStatus = "ok"
	RunStageSkipped      RunStageStatus = "skipped"
	RunStageFailed       RunStageStatus = "failed"
	RunStageInconclusive RunStageStatus = "inconclusive"
)

// RunStageStatusValues lists all stage statuses in canonical order.
var RunStageStatusValues = []RunStageStatus{
	RunStageOk, RunStageSkipped, RunStageFailed, RunStageInconclusive,
}

// IsValidRunStageStatus reports membership in RUN_STAGE_STATUS.
func IsValidRunStageStatus(s string) bool {
	for _, v := range RunStageStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// RunStage is one stage record (RUN_STAGE_SCHEMA).
type RunStage struct {
	Stage  string         `json:"stage"`
	Status RunStageStatus `json:"status"`
	Note   *string        `json:"note,omitempty"`
}

// Validate checks the stage against RUN_STAGE_SCHEMA (strict).
func (s *RunStage) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(s.Stage, 1, []string{"stage"}))
	Collect(&issues, CheckEnum(string(s.Status), stringsOf(RunStageStatusValues), []string{"status"}))
	if s.Note != nil {
		Collect(&issues, RequiredString(*s.Note, 1, []string{"note"}))
	}
	return issues
}

// RunKey is the idempotency key (RUN_KEY_SCHEMA).
type RunKey struct {
	Repo            string `json:"repo"`
	BaseSHA         string `json:"base_sha"`
	HeadSHA         string `json:"head_sha"`
	ManifestVersion int    `json:"manifest_version"`
	PolicyVersion   int    `json:"policy_version"`
}

// Validate checks the key against RUN_KEY_SCHEMA (strict).
func (k *RunKey) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(k.Repo, 1, []string{"repo"}))
	Collect(&issues, RequiredString(k.BaseSHA, 1, []string{"base_sha"}))
	Collect(&issues, RequiredString(k.HeadSHA, 1, []string{"head_sha"}))
	if k.ManifestVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"manifest_version"}, Message: "Number must be greater than 0"})
	}
	if k.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// StateChurn holds the A8 state counters (STATE_CHURN_SCHEMA).
type StateChurn struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// Validate checks the counters against STATE_CHURN_SCHEMA (strict).
func (c *StateChurn) Validate() []Issue {
	var issues []Issue
	if c.Added < 0 {
		Collect(&issues, &Issue{Path: []string{"added"}, Message: "Number must be greater than or equal to 0"})
	}
	if c.Updated < 0 {
		Collect(&issues, &Issue{Path: []string{"updated"}, Message: "Number must be greater than or equal to 0"})
	}
	if c.Unchanged < 0 {
		Collect(&issues, &Issue{Path: []string{"unchanged"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// RunRecordSchemaID is the literal schema tag of `docsync.run/v1`.
const RunRecordSchemaID = "docsync.run/v1"

// RunRecord is the `docsync.run/v1` record (RUN_RECORD_SCHEMA).
type RunRecord struct {
	Schema     string     `json:"schema"`
	RunID      string     `json:"run_id"`
	Key        RunKey     `json:"key"`
	StartedAt  string     `json:"started_at"`
	FinishedAt *string    `json:"finished_at,omitempty"`
	Outcome    *string    `json:"outcome,omitempty"`
	StateChurn StateChurn `json:"state_churn"`
	Stages     []RunStage `json:"stages"`
}

// Validate checks the record against RUN_RECORD_SCHEMA (strict), reproducing
// the zod issue paths.
func (r *RunRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != RunRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.run/v1"`})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	for _, iss := range r.Key.Validate() {
		iss.Path = append([]string{"key"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, datetimeIssue(r.StartedAt, []string{"started_at"}))
	if r.FinishedAt != nil {
		Collect(&issues, datetimeIssue(*r.FinishedAt, []string{"finished_at"}))
	}
	if r.Outcome != nil {
		Collect(&issues, RequiredString(*r.Outcome, 1, []string{"outcome"}))
	}
	for _, iss := range r.StateChurn.Validate() {
		iss.Path = append([]string{"state_churn"}, iss.Path...)
		issues = append(issues, iss)
	}
	for i := range r.Stages {
		for _, iss := range r.Stages[i].Validate() {
			iss.Path = append([]string{"stages", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ParseRunRecord strictly decodes and validates `docsync.run/v1` bytes
// (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseRunRecord(data []byte) (*RunRecord, error) {
	var r RunRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// RunRecordExample mirrors RUN_RECORD_EXAMPLE (registered as
// 'docsync.run-record' v1; golden data for byte-for-byte serializer checks).
func RunRecordExample() RunRecord {
	finishedAt := "2026-10-04T22:30:44Z"
	outcome := "review"
	return RunRecord{
		Schema: "docsync.run/v1",
		RunID:  "run_01J9ZK6X8A",
		Key: RunKey{
			Repo:            "acme/payments",
			BaseSHA:         "8a73f2c",
			HeadSHA:         "81af92e",
			ManifestVersion: 1,
			PolicyVersion:   17,
		},
		StartedAt:  "2026-10-04T22:30:41Z",
		FinishedAt: &finishedAt,
		Outcome:    &outcome,
		StateChurn: StateChurn{Added: 1, Updated: 2, Unchanged: 19},
		Stages: []RunStage{
			{Stage: "change-analysis", Status: RunStageOk},
			{Stage: "decision", Status: RunStageOk},
			{Stage: "writer", Status: RunStageSkipped, Note: strPtr("abstained: low confidence")},
		},
	}
}

// strPtr returns a pointer to s (local literal helper for the examples).
func strPtr(s string) *string { return &s }

/* ------------------------------------------------- shared schema helpers */

// stringsOf converts a typed value slice (e.g. VerificationVerdictValues)
// into []string so CheckEnum can render zod's exact enum message. Reuse
// this instead of re-deriving the conversion in every Validate().
func stringsOf[T ~string](vals []T) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}

// isRFC3339UTCDatetime reports whether v satisfies zod z.string().datetime()
// as pinned by the TS suite (provenance.test.ts: "rejects non-UTC datetimes
// without offset support" — `2026-10-04T22:31:00+02:00` is rejected): strict
// YYYY-MM-DDTHH:MM:SS with optional fractional seconds and a 'Z' suffix.
// Calendar validity is enforced as well (fail closed).
func isRFC3339UTCDatetime(v string) bool {
	if len(v) < 20 || !strings.HasSuffix(v, "Z") {
		return false
	}
	body := v[:len(v)-1]
	if dot := strings.IndexByte(body, '.'); dot >= 0 {
		frac := body[dot+1:]
		if frac == "" {
			return false
		}
		for i := 0; i < len(frac); i++ {
			if frac[i] < '0' || frac[i] > '9' {
				return false
			}
		}
		body = body[:dot]
	}
	if len(body) != len("2006-01-02T15:04:05") {
		return false
	}
	t, err := time.Parse("2006-01-02T15:04:05", body)
	if err != nil {
		return false
	}
	return t.Format("2006-01-02T15:04:05") == body
}

// datetimeIssue renders zod's datetime failure ("Invalid datetime") for a
// required datetime field.
func datetimeIssue(v string, path []string) *Issue {
	if !isRFC3339UTCDatetime(v) {
		return &Issue{Path: path, Message: "Invalid datetime"}
	}
	return nil
}

// optionalDatetimeIssue is the nullable variant of datetimeIssue
// (z.string().datetime().optional() / .nullable()).
func optionalDatetimeIssue(v *string, path []string) *Issue {
	if v == nil {
		return nil
	}
	return datetimeIssue(*v, path)
}
