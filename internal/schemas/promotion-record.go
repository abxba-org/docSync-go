package schemas

import "github.com/DUDE616/docSync-go/internal/docutil"

// Ported from packages/schemas/src/v1/promotion-record.ts
// (PROMOTION_RECORD_SCHEMA_ID + PROMOTION_STATUS_VALUES +
// PROMOTION_EVALUATION_SCHEMA + PROMOTION_RECORD_SCHEMA +
// PROMOTION_RECORD_EXAMPLE).
//
// Promotion record (P10-T01; ARCH §8.4/§8.5, CTX §19/§20): one versioned,
// human-approved policy promotion in `.docsync/policy-history/`. MODELS
// PROPOSE; HUMANS PROMOTE (INV-7) — approved/promoted REQUIRES approved_by.
// Rollback = promoting the prior version, a one-operation metadata act.
// `unsafe_auto_updates` is a GATE METRIC — not promotable away regardless of
// precision/recall (ARCH §8.4).
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules).

// PromotionRecordSchemaID is the literal schema tag
// (PROMOTION_RECORD_SCHEMA_ID).
const PromotionRecordSchemaID = "docsync.promotion-record/v1"

// PromotionStatus is the closed promotion-status vocabulary
// (PROMOTION_STATUS_VALUES).
type PromotionStatus string

// The promotion statuses in canonical order (PROMOTION_STATUS_VALUES).
const (
	PromotionStatusProposed   PromotionStatus = "proposed"
	PromotionStatusApproved   PromotionStatus = "approved"
	PromotionStatusPromoted   PromotionStatus = "promoted"
	PromotionStatusRolledBack PromotionStatus = "rolled_back"
)

// PromotionStatusValues lists all statuses in canonical order.
var PromotionStatusValues = []PromotionStatus{
	PromotionStatusProposed, PromotionStatusApproved, PromotionStatusPromoted, PromotionStatusRolledBack,
}

// IsValidPromotionStatus reports membership in PROMOTION_STATUS_VALUES.
func IsValidPromotionStatus(s string) bool {
	for _, v := range PromotionStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// PromotionEvaluation is the embedded evaluation evidence
// (PROMOTION_EVALUATION_SCHEMA) — full report + the two gate inputs.
type PromotionEvaluation struct {
	Report            map[string]any `json:"report"`
	UnsafeAutoUpdates int            `json:"unsafe_auto_updates"`
	SampleSize        int            `json:"sample_size"`
}

// Validate checks the evaluation evidence against
// PROMOTION_EVALUATION_SCHEMA (strict).
func (e *PromotionEvaluation) Validate() []Issue {
	var issues []Issue
	if e.Report == nil {
		Collect(&issues, &Issue{Path: []string{"report"}, Message: "Required"})
	}
	if e.UnsafeAutoUpdates < 0 {
		Collect(&issues, &Issue{Path: []string{"unsafe_auto_updates"}, Message: "Number must be greater than or equal to 0"})
	}
	if e.SampleSize < 1 {
		Collect(&issues, &Issue{Path: []string{"sample_size"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// PromotionRecord is the `docsync.promotion-record/v1` record
// (PROMOTION_RECORD_SCHEMA).
type PromotionRecord struct {
	Schema          string              `json:"schema"`
	PromotionID     string              `json:"promotion_id"`
	PolicyVersion   int                 `json:"policy_version"`
	PreviousVersion *int                `json:"previous_version,omitempty"`
	Reason          string              `json:"reason"`
	Evaluation      PromotionEvaluation `json:"evaluation"`
	Status          PromotionStatus     `json:"status"`
	ApprovedBy      *string             `json:"approved_by,omitempty"`
	At              string              `json:"at"`
}

// Validate checks the record against PROMOTION_RECORD_SCHEMA (strict),
// reproducing the zod issue paths and the INV-7 superRefine.
func (r *PromotionRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != PromotionRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.promotion-record/v1"`})
	}
	if !docutil.IsValidULID(r.PromotionID) {
		Collect(&issues, &Issue{Path: []string{"promotion_id"}, Message: "Invalid"})
	}
	if r.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	if r.PreviousVersion != nil && *r.PreviousVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"previous_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(r.Reason, 1, []string{"reason"}))
	for _, iss := range r.Evaluation.Validate() {
		iss.Path = append([]string{"evaluation"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, CheckEnum(string(r.Status), stringsOf(PromotionStatusValues), []string{"status"}))
	if r.ApprovedBy != nil {
		Collect(&issues, RequiredString(*r.ApprovedBy, 1, []string{"approved_by"}))
	}
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	// INV-7 superRefine: models propose, humans promote.
	if (r.Status == PromotionStatusApproved || r.Status == PromotionStatusPromoted) && r.ApprovedBy == nil {
		Collect(&issues, &Issue{
			Message: "status approved/promoted requires approved_by — models propose, humans promote (INV-7)",
		})
	}
	return issues
}

// ParsePromotionRecord strictly decodes and validates
// `docsync.promotion-record/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParsePromotionRecord(data []byte) (*PromotionRecord, error) {
	var r PromotionRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// PromotionRecordExample mirrors PROMOTION_RECORD_EXAMPLE (golden data for
// byte-for-byte serializer checks).
func PromotionRecordExample() PromotionRecord {
	return PromotionRecord{
		Schema:          "docsync.promotion-record/v1",
		PromotionID:     "01JPR9M9T19NEXAMP1E0000000",
		PolicyVersion:   2,
		PreviousVersion: new(1),
		Reason:          "offline eval wins precision +0.06 with no regression and unsafe_auto_updates=0",
		Evaluation: PromotionEvaluation{
			Report: map[string]any{
				"policy_version":   2,
				"baseline_version": 1,
				"sample_size":      140,
			},
			UnsafeAutoUpdates: 0,
			SampleSize:        140,
		},
		Status:     PromotionStatusPromoted,
		ApprovedBy: new("repo-owner:@acme/maintainers"),
		At:         "2026-10-08T12:00:00.000Z",
	}
}
