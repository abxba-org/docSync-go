package schemas

// Ported from packages/schemas/src/v1/canary-record.ts
// (CANARY_RECORD_SCHEMA_ID + CANARY_STATUS_VALUES + CANARY_RECORD_SCHEMA +
// CANARY_RECORD_EXAMPLE).
//
// Canary lifecycle record — `docsync.canary-record/v1` (P12-T05; ARCH §8.6,
// CTX §21): the durable canary status row persisted under
// `.docsync/policy-history/` (the same record layer as the promotion event
// log). Time-boxed canaries: every row carries `created_at` and `expires_at`
// (mirroring the W1 `docsync.shadow-run-record/v1` time-box data); a canary
// whose `expires_at` has passed is AUTO-EXPIRED by the lifecycle enforcer —
// `status: 'expired'` is a TYPED terminal outcome and an expired canary is
// never consulted again and never promotable (ARCH §8.6 "time-box canaries;
// auto-expire stale shadows").
//
// Append-only transition rows exactly like `docsync.promotion-record/v1`: the
// LATEST row per `canary_id` is the canary's current status
// (`active` → `expired` | `promoted` | `rejected`). `promoted` here means the
// canary CONCLUDED successfully and its evidence was handed to the promotion
// pipeline (which owns the actual policy promotion, INV-7) — this record
// never promotes policy by itself. Candidate and production versions must
// differ (a canary compares two policies) and `expires_at` must be strictly
// after `created_at` — both fail closed via the superRefine.
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules).

import (
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// CanaryRecordSchemaID is the literal schema tag (CANARY_RECORD_SCHEMA_ID).
const CanaryRecordSchemaID = "docsync.canary-record/v1"

// CanaryStatus is the closed canary-status vocabulary (CANARY_STATUS_VALUES).
type CanaryStatus string

// The canary statuses in canonical order.
const (
	CanaryStatusActive   CanaryStatus = "active"
	CanaryStatusExpired  CanaryStatus = "expired"
	CanaryStatusPromoted CanaryStatus = "promoted"
	CanaryStatusRejected CanaryStatus = "rejected"
)

// CanaryStatusValues lists all statuses in canonical order.
var CanaryStatusValues = []CanaryStatus{
	CanaryStatusActive, CanaryStatusExpired, CanaryStatusPromoted, CanaryStatusRejected,
}

// IsValidCanaryStatus reports membership in CANARY_STATUS_VALUES.
func IsValidCanaryStatus(s string) bool {
	for _, v := range CanaryStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// CanaryRecord is the `docsync.canary-record/v1` record (CANARY_RECORD_SCHEMA).
type CanaryRecord struct {
	Schema                  string       `json:"schema"`
	CanaryID                string       `json:"canary_id"`
	RunID                   string       `json:"run_id"`
	Repo                    string       `json:"repo"`
	CandidatePolicyVersion  int          `json:"candidate_policy_version"`
	ProductionPolicyVersion int          `json:"production_policy_version"`
	Status                  CanaryStatus `json:"status"`
	Reason                  string       `json:"reason"`
	CreatedAt               string       `json:"created_at"`
	ExpiresAt               string       `json:"expires_at"`
	At                      string       `json:"at"`
}

// Validate checks the record against CANARY_RECORD_SCHEMA (strict),
// reproducing the zod issue paths and the two superRefine cross-checks.
func (r *CanaryRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != CanaryRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.canary-record/v1"`})
	}
	if !docutil.IsValidULID(r.CanaryID) {
		Collect(&issues, &Issue{Path: []string{"canary_id"}, Message: "Invalid"})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, RequiredString(r.Repo, 1, []string{"repo"}))
	if r.CandidatePolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"candidate_policy_version"}, Message: "Number must be greater than 0"})
	}
	if r.ProductionPolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"production_policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, CheckEnum(string(r.Status), stringsOf(CanaryStatusValues), []string{"status"}))
	Collect(&issues, RequiredString(r.Reason, 1, []string{"reason"}))
	Collect(&issues, datetimeIssue(r.CreatedAt, []string{"created_at"}))
	Collect(&issues, datetimeIssue(r.ExpiresAt, []string{"expires_at"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	if r.CandidatePolicyVersion == r.ProductionPolicyVersion {
		Collect(&issues, &Issue{
			Path:    []string{"candidate_policy_version"},
			Message: "candidate and production policy versions must differ — a canary compares two policies",
		})
	}
	if created, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
		if expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil {
			if !expires.After(created) {
				Collect(&issues, &Issue{
					Path:    []string{"expires_at"},
					Message: "expires_at must be strictly after created_at (time-boxed canary, ARCH §8.6)",
				})
			}
		}
	}
	return issues
}

// ParseCanaryRecord strictly decodes and validates `docsync.canary-record/v1`
// bytes (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseCanaryRecord(data []byte) (*CanaryRecord, error) {
	var r CanaryRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// CanaryRecordExample mirrors CANARY_RECORD_EXAMPLE (golden data for
// byte-for-byte serializer checks).
func CanaryRecordExample() CanaryRecord {
	return CanaryRecord{
		Schema:                  "docsync.canary-record/v1",
		CanaryID:                "01JCANARYEXAMP1ES000000000",
		RunID:                   "shadow-v18-canary-01",
		Repo:                    "acme/widgets",
		CandidatePolicyVersion:  18,
		ProductionPolicyVersion: 17,
		Status:                  CanaryStatusActive,
		Reason:                  "canary opened: sampled live shadow run active within its time-box",
		CreatedAt:               "2026-10-09T00:00:00.000Z",
		ExpiresAt:               "2026-10-23T00:00:00.000Z",
		At:                      "2026-10-09T00:00:00.000Z",
	}
}
