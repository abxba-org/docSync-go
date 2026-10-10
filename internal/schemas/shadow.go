package schemas

// Ported from packages/schemas/src/v1/shadow-agreement-matrix.ts
// (SHADOW_AGREEMENT_MATRIX_SCHEMA_ID + SHADOW_DISAGREEMENT_RESOLUTIONS +
// SHADOW_INPUT_HASH_REGEX + SHADOW_INPUT_HASH + SHADOW_CASE_INPUT_HASHES_SCHEMA
// + SHADOW_MATRIX_CELL_SCHEMA + SHADOW_MATRIX_DISAGREEMENT_SCHEMA +
// SHADOW_AGREEMENT_MATRIX_SCHEMA + SHADOW_AGREEMENT_MATRIX_EXAMPLE) and
// packages/schemas/src/v1/shadow-run-record.ts
// (SHADOW_RUN_RECORD_SCHEMA_ID + SHADOW_SAMPLER_VALUES + SHADOW_SAMPLING_SCHEMA
// + SHADOW_DECISION_SCHEMA + SHADOW_RECORDED_INPUT_HASHES_SCHEMA +
// SHADOW_RUN_RECORD_SCHEMA + SHADOW_RUN_RECORD_EXAMPLE).
//
// Shadow artifacts (P12-T02/P12-T04; ARCH §8.6; D6).
//
// Agreement matrix — `docsync.shadow-agreement-matrix/v1`: the decision-level
// candidate-vs-production comparison artifact over one replay/live-shadow
// split. The decision vocabulary is IMPORTED from `docsync.decision/v1`
// (`ActionValues`, OD-2: import, never duplicate). `input_hashes` records, per
// case, the input hash each policy observed — `equal` is a LITERAL `true`
// because a matrix record can only exist when candidate and production
// received identical inputs. Disagreement resolutions carry a closed
// vocabulary; `pending` is the only value a producer may write without
// reviewer evidence.
//
// Shadow run record — `docsync.shadow-run-record/v1`: the record side of the
// SAMPLED live shadow. The live shadow runs the candidate policy beside
// production on a deterministic sample of PRs and records its decisions HERE —
// shadow-only data that is NEVER surfaced to developers (the literal
// `shadow_only: true` marker encodes the contract). Sampling is fully recorded
// for auditability (sampler id, fraction, deterministic draw/threshold hex
// pair); eligibility is recorded data (OD-R29: canary/auto repos only); the
// record carries the time-box (`created_at`/`expires_at`).
//
// Wire keys stay snake_case exactly as zod declared them; z.strictObject ⇒
// unknown JSON keys are rejected at parse (StrictDecode), and every zod
// validation message is reproduced in Validate() via Issue paths (see
// decode.go for the zod→Go mapping rules).

import (
	"regexp"
	"sort"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// shadowExampleInputHash is the example's 64-hex sha256 input hash
// (SHADOW_AGREEMENT_MATRIX_EXAMPLE's EXAMPLE_HASH /
// SHADOW_RUN_RECORD_EXAMPLE's per-stage hashes — the same literal).
const shadowExampleInputHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// ShadowAgreementMatrixSchemaID is the literal schema tag
// (SHADOW_AGREEMENT_MATRIX_SCHEMA_ID).
const ShadowAgreementMatrixSchemaID = "docsync.shadow-agreement-matrix/v1"

// ShadowDisagreementResolution is the closed disagreement-resolution
// vocabulary (SHADOW_DISAGREEMENT_RESOLUTIONS): `pending` until a reviewer
// outcome exists; the three resolved labels name which policy was right.
type ShadowDisagreementResolution string

// The disagreement resolutions in canonical order.
const (
	ShadowResolutionPending           ShadowDisagreementResolution = "pending"
	ShadowResolutionCandidateCorrect  ShadowDisagreementResolution = "candidate_correct"
	ShadowResolutionProductionCorrect ShadowDisagreementResolution = "production_correct"
	ShadowResolutionBothWrong         ShadowDisagreementResolution = "both_wrong"
)

// ShadowDisagreementResolutionValues lists all resolutions in canonical order.
var ShadowDisagreementResolutionValues = []ShadowDisagreementResolution{
	ShadowResolutionPending, ShadowResolutionCandidateCorrect,
	ShadowResolutionProductionCorrect, ShadowResolutionBothWrong,
}

// IsValidShadowDisagreementResolution reports membership.
func IsValidShadowDisagreementResolution(s string) bool {
	for _, v := range ShadowDisagreementResolutionValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// IsShadowInputHash reports the sha256-hex shape recorded by the
// identical-input guarantee (SHADOW_INPUT_HASH_REGEX /^[0-9a-f]{64}$/ —
// V-P12-2). Same shape as hex.go's shared BareSHA256Hex leaf, reused here
// per the package's single-definition rule.
func IsShadowInputHash(s string) bool { return IsBareSHA256Hex(s) }

// ShadowCaseInputHashes is the per-case recorded input hashes
// (SHADOW_CASE_INPUT_HASHES_SCHEMA) — `equal: true` is mandatory by
// construction.
type ShadowCaseInputHashes struct {
	Production string `json:"production"`
	Candidate  string `json:"candidate"`
	Equal      bool   `json:"equal"`
}

// Validate checks the case input hashes (strict).
func (h *ShadowCaseInputHashes) Validate() []Issue {
	var issues []Issue
	if !IsShadowInputHash(h.Production) {
		Collect(&issues, &Issue{Path: []string{"production"}, Message: "Invalid"})
	}
	if !IsShadowInputHash(h.Candidate) {
		Collect(&issues, &Issue{Path: []string{"candidate"}, Message: "Invalid"})
	}
	if !h.Equal {
		Collect(&issues, &Issue{Path: []string{"equal"}, Message: `Invalid literal value, expected true`})
	}
	return issues
}

// ShadowMatrixCell is one non-empty matrix cell
// (SHADOW_MATRIX_CELL_SCHEMA): a (candidate, production) decision pair + its
// cases.
type ShadowMatrixCell struct {
	CandidateDecision  Action `json:"candidate_decision"`
	ProductionDecision Action `json:"production_decision"`
	Count              int    `json:"count"`
	// CaseIDs are the case ids contributing to the cell (sorted, unique).
	CaseIDs []string `json:"case_ids"`
}

// Validate checks the cell (strict).
func (c *ShadowMatrixCell) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(c.CandidateDecision), stringsOf(ActionValues), []string{"candidate_decision"}))
	Collect(&issues, CheckEnum(string(c.ProductionDecision), stringsOf(ActionValues), []string{"production_decision"}))
	if c.Count < 1 {
		Collect(&issues, &Issue{Path: []string{"count"}, Message: "Number must be greater than 0"})
	}
	for i, caseID := range c.CaseIDs {
		Collect(&issues, RequiredString(caseID, 1, []string{"case_ids", itoa(i)}))
	}
	return issues
}

// ShadowMatrixDisagreement is one disagreement row (SHADOW_MATRIX_DISAGREEMENT_SCHEMA),
// resolvable to a label once reviewer outcomes exist.
type ShadowMatrixDisagreement struct {
	CaseID             string                       `json:"case_id"`
	CandidateDecision  Action                       `json:"candidate_decision"`
	ProductionDecision Action                       `json:"production_decision"`
	Resolution         ShadowDisagreementResolution `json:"resolution"`
}

// Validate checks the disagreement row (strict).
func (d *ShadowMatrixDisagreement) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.CaseID, 1, []string{"case_id"}))
	Collect(&issues, CheckEnum(string(d.CandidateDecision), stringsOf(ActionValues), []string{"candidate_decision"}))
	Collect(&issues, CheckEnum(string(d.ProductionDecision), stringsOf(ActionValues), []string{"production_decision"}))
	Collect(&issues, CheckEnum(string(d.Resolution), stringsOf(ShadowDisagreementResolutionValues), []string{"resolution"}))
	return issues
}

// ShadowAgreementMatrix is the `docsync.shadow-agreement-matrix/v1` artifact
// (SHADOW_AGREEMENT_MATRIX_SCHEMA).
type ShadowAgreementMatrix struct {
	Schema                  string                           `json:"schema"`
	ProductionPolicyVersion int                              `json:"production_policy_version"`
	CandidatePolicyVersion  int                              `json:"candidate_policy_version"`
	SplitID                 string                           `json:"split_id"`
	Cells                   []ShadowMatrixCell               `json:"cells"`
	AgreementCount          int                              `json:"agreement_count"`
	DisagreementCount       int                              `json:"disagreement_count"`
	InputHashes             map[string]ShadowCaseInputHashes `json:"input_hashes"`
	Disagreements           []ShadowMatrixDisagreement       `json:"disagreements"`
	ComputedAt              string                           `json:"computed_at"`
}

// Validate checks the matrix against SHADOW_AGREEMENT_MATRIX_SCHEMA (strict),
// reproducing the zod issue paths and the superRefine cross-checks.
func (m *ShadowAgreementMatrix) Validate() []Issue {
	var issues []Issue
	if m.Schema != ShadowAgreementMatrixSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.shadow-agreement-matrix/v1"`})
	}
	if m.ProductionPolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"production_policy_version"}, Message: "Number must be greater than 0"})
	}
	if m.CandidatePolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"candidate_policy_version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(m.SplitID, 1, []string{"split_id"}))
	for i := range m.Cells {
		for _, iss := range m.Cells[i].Validate() {
			iss.Path = append([]string{"cells", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if m.AgreementCount < 0 {
		Collect(&issues, &Issue{Path: []string{"agreement_count"}, Message: "Number must be greater than or equal to 0"})
	}
	if m.DisagreementCount < 0 {
		Collect(&issues, &Issue{Path: []string{"disagreement_count"}, Message: "Number must be greater than or equal to 0"})
	}
	// z.record(z.string().min(1), …): empty record keys are refused.
	for _, key := range sortedShadowInputHashKeys(m.InputHashes) {
		Collect(&issues, RequiredString(key, 1, []string{"input_hashes", key}))
		hashes := m.InputHashes[key]
		for _, iss := range hashes.Validate() {
			iss.Path = append([]string{"input_hashes", key}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range m.Disagreements {
		for _, iss := range m.Disagreements[i].Validate() {
			iss.Path = append([]string{"disagreements", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, datetimeIssue(m.ComputedAt, []string{"computed_at"}))

	// Counts consistency: the cells must classify exactly the cases counted.
	total := 0
	for _, cell := range m.Cells {
		total += cell.Count
	}
	if total != m.AgreementCount+m.DisagreementCount {
		Collect(&issues, &Issue{
			Path:    []string{"agreement_count"},
			Message: "cells must classify exactly agreement_count + disagreement_count cases",
		})
	}
	// Every case appears in exactly one cell (case ids unique across cells).
	seen := make(map[string]struct{})
	for _, cell := range m.Cells {
		for _, caseID := range cell.CaseIDs {
			if _, dup := seen[caseID]; dup {
				Collect(&issues, &Issue{
					Path:    []string{"cells"},
					Message: "case " + caseID + " appears in more than one cell",
				})
			}
			seen[caseID] = struct{}{}
		}
	}
	// Disagreement rows must mirror the mixed cells exactly — one row per
	// disagreeing case, no rows for agreeing cases (fail closed on drift).
	// JS Map keeps the LAST row per case_id; Go map assignment matches.
	byCase := make(map[string]ShadowMatrixDisagreement, len(m.Disagreements))
	for _, row := range m.Disagreements {
		byCase[row.CaseID] = row
	}
	if len(byCase) != len(m.Disagreements) {
		Collect(&issues, &Issue{
			Path:    []string{"disagreements"},
			Message: "duplicate case_id in disagreements",
		})
	}
	for _, cell := range m.Cells {
		agrees := cell.CandidateDecision == cell.ProductionDecision
		for _, caseID := range cell.CaseIDs {
			row, ok := byCase[caseID]
			if agrees && ok {
				Collect(&issues, &Issue{
					Path:    []string{"disagreements"},
					Message: "case " + caseID + " is in an agreeing cell but carries a disagreement row",
				})
			}
			if !agrees && (!ok || row.CandidateDecision != cell.CandidateDecision || row.ProductionDecision != cell.ProductionDecision) {
				Collect(&issues, &Issue{
					Path:    []string{"disagreements"},
					Message: "disagreeing case " + caseID + " has no matching disagreement row",
				})
			}
		}
	}
	for _, row := range m.Disagreements {
		backed := false
		for _, cell := range m.Cells {
			if cell.CandidateDecision != row.CandidateDecision || cell.ProductionDecision != row.ProductionDecision {
				continue
			}
			for _, caseID := range cell.CaseIDs {
				if caseID == row.CaseID {
					backed = true
					break
				}
			}
			if backed {
				break
			}
		}
		if !backed {
			Collect(&issues, &Issue{
				Path:    []string{"disagreements"},
				Message: "disagreement case " + row.CaseID + " is not backed by a mixed cell",
			})
		}
	}
	return issues
}

// sortedShadowInputHashKeys returns the record keys in deterministic order so
// issue ordering is stable (Go map iteration is randomized; zod iterates in
// insertion order and only reports the first failing key).
func sortedShadowInputHashKeys(m map[string]ShadowCaseInputHashes) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ParseShadowAgreementMatrix strictly decodes and validates
// `docsync.shadow-agreement-matrix/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseShadowAgreementMatrix(data []byte) (*ShadowAgreementMatrix, error) {
	var m ShadowAgreementMatrix
	if err := StrictDecode(data, &m); err != nil {
		return nil, err
	}
	if issues := m.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &m, nil
}

// ShadowAgreementMatrixExample mirrors SHADOW_AGREEMENT_MATRIX_EXAMPLE
// (golden data for byte-for-byte serializer checks).
func ShadowAgreementMatrixExample() ShadowAgreementMatrix {
	return ShadowAgreementMatrix{
		Schema:                  "docsync.shadow-agreement-matrix/v1",
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		SplitID:                 "frozen-42-shadow-fixture",
		Cells: []ShadowMatrixCell{
			{CandidateDecision: ActionIgnore, ProductionDecision: ActionIgnore, Count: 2, CaseIDs: []string{"case-01", "case-02"}},
			{CandidateDecision: ActionReview, ProductionDecision: ActionReview, Count: 1, CaseIDs: []string{"case-03"}},
			{CandidateDecision: ActionReview, ProductionDecision: ActionIgnore, Count: 1, CaseIDs: []string{"case-04"}},
		},
		AgreementCount:    3,
		DisagreementCount: 1,
		InputHashes: map[string]ShadowCaseInputHashes{
			"case-01": {Production: shadowExampleInputHash, Candidate: shadowExampleInputHash, Equal: true},
			"case-02": {Production: shadowExampleInputHash, Candidate: shadowExampleInputHash, Equal: true},
			"case-03": {Production: shadowExampleInputHash, Candidate: shadowExampleInputHash, Equal: true},
			"case-04": {Production: shadowExampleInputHash, Candidate: shadowExampleInputHash, Equal: true},
		},
		Disagreements: []ShadowMatrixDisagreement{
			{
				CaseID:             "case-04",
				CandidateDecision:  ActionReview,
				ProductionDecision: ActionIgnore,
				Resolution:         ShadowResolutionPending,
			},
		},
		ComputedAt: "2026-10-09T00:00:00.000Z",
	}
}

// ShadowRunRecordSchemaID is the literal schema tag
// (SHADOW_RUN_RECORD_SCHEMA_ID).
const ShadowRunRecordSchemaID = "docsync.shadow-run-record/v1"

// ShadowSampler is the closed deterministic-sampler vocabulary
// (SHADOW_SAMPLER_VALUES).
type ShadowSampler string

// The shadow sampler values in canonical order.
const ShadowSamplerSHA256HexThreshold ShadowSampler = "sha256-hex-threshold/v1"

// ShadowSamplerValues lists all sampler ids in canonical order.
var ShadowSamplerValues = []ShadowSampler{ShadowSamplerSHA256HexThreshold}

// IsValidShadowSampler reports membership.
func IsValidShadowSampler(s string) bool {
	for _, v := range ShadowSamplerValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

var (
	// shadowDrawHexRe is `draw_hex`'s first-8-hex-chars shape
	// (SHADOW_SAMPLING_SCHEMA /^[0-9a-f]{8}$/).
	shadowDrawHexRe = regexp.MustCompile(`^[0-9a-f]{8}$`)
	// shadowThresholdHexRe is `threshold_hex`'s 8-9 hex shape (the fraction=1
	// edge is 9 chars).
	shadowThresholdHexRe = regexp.MustCompile(`^[0-9a-f]{8,9}$`)
)

// ShadowSampling is the deterministic sampling record
// (SHADOW_SAMPLING_SCHEMA): the sampler id, the configured fraction, and the
// draw/threshold hex pair. No randomness — the draw is a sha256 hex slice
// keyed by run id.
type ShadowSampling struct {
	Sampler      ShadowSampler `json:"sampler"`
	Fraction     float64       `json:"fraction"`
	DrawHex      string        `json:"draw_hex"`
	ThresholdHex string        `json:"threshold_hex"`
}

// Validate checks the sampling block (strict).
func (s *ShadowSampling) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(s.Sampler), stringsOf(ShadowSamplerValues), []string{"sampler"}))
	if s.Fraction < 0 {
		Collect(&issues, &Issue{Path: []string{"fraction"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.Fraction > 1 {
		Collect(&issues, &Issue{Path: []string{"fraction"}, Message: "Number must be less than or equal to 1"})
	}
	if !shadowDrawHexRe.MatchString(s.DrawHex) {
		Collect(&issues, &Issue{Path: []string{"draw_hex"}, Message: "Invalid"})
	}
	if !shadowThresholdHexRe.MatchString(s.ThresholdHex) {
		Collect(&issues, &Issue{Path: []string{"threshold_hex"}, Message: "Invalid"})
	}
	return issues
}

// ShadowDecision is the recorded shadow decision (SHADOW_DECISION_SCHEMA).
type ShadowDecision struct {
	Action        Action         `json:"action"`
	Abstained     bool           `json:"abstained"`
	AbstainReason *AbstainReason `json:"abstain_reason,omitempty"`
}

// Validate checks the recorded decision (strict).
func (d *ShadowDecision) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(d.Action), stringsOf(ActionValues), []string{"action"}))
	if d.AbstainReason != nil {
		Collect(&issues, CheckEnum(string(*d.AbstainReason), stringsOf(AbstainReasonValues), []string{"abstain_reason"}))
	}
	return issues
}

// ShadowRecordedInputHashes is the V-P12-2 lineage block
// (SHADOW_RECORDED_INPUT_HASHES_SCHEMA): the input hashes the shadow decision
// was computed over.
type ShadowRecordedInputHashes struct {
	ChangeAnalysis string `json:"change_analysis"`
	CandidateSet   string `json:"candidate_set"`
	DiffSubset     string `json:"diff_subset"`
}

// Validate checks the recorded input hashes (strict).
func (h *ShadowRecordedInputHashes) Validate() []Issue {
	var issues []Issue
	if !IsShadowInputHash(h.ChangeAnalysis) {
		Collect(&issues, &Issue{Path: []string{"change_analysis"}, Message: "Invalid"})
	}
	if !IsShadowInputHash(h.CandidateSet) {
		Collect(&issues, &Issue{Path: []string{"candidate_set"}, Message: "Invalid"})
	}
	if !IsShadowInputHash(h.DiffSubset) {
		Collect(&issues, &Issue{Path: []string{"diff_subset"}, Message: "Invalid"})
	}
	return issues
}

// ShadowRunRecord is the `docsync.shadow-run-record/v1` record
// (SHADOW_RUN_RECORD_SCHEMA).
type ShadowRunRecord struct {
	Schema      string                     `json:"schema"`
	RecordID    string                     `json:"record_id"`
	RunID       string                     `json:"run_id"`
	PrID        string                     `json:"pr_id"`
	Repo        string                     `json:"repo"`
	ShadowOnly  bool                       `json:"shadow_only"`
	Sampled     bool                       `json:"sampled"`
	Eligible    bool                       `json:"eligible"`
	SkipReason  *string                    `json:"skip_reason,omitempty"`
	Sampling    ShadowSampling             `json:"sampling"`
	Policy      ShadowRecordedPolicy       `json:"policy"`
	Decision    *ShadowDecision            `json:"decision,omitempty"`
	InputHashes *ShadowRecordedInputHashes `json:"input_hashes,omitempty"`
	CreatedAt   string                     `json:"created_at"`
	ExpiresAt   string                     `json:"expires_at"`
}

// ShadowRecordedPolicy is the inline policy block of SHADOW_RUN_RECORD_SCHEMA
// (the candidate/production versions the record compared).
type ShadowRecordedPolicy struct {
	ProductionVersion int `json:"production_version"`
	CandidateVersion  int `json:"candidate_version"`
}

// Validate checks the policy block (strict).
func (p *ShadowRecordedPolicy) Validate() []Issue {
	var issues []Issue
	if p.ProductionVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"production_version"}, Message: "Number must be greater than 0"})
	}
	if p.CandidateVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"candidate_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// Validate checks the record against SHADOW_RUN_RECORD_SCHEMA (strict),
// reproducing the zod issue paths and the sampled/unsampled superRefine.
func (r *ShadowRunRecord) Validate() []Issue {
	var issues []Issue
	if r.Schema != ShadowRunRecordSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.shadow-run-record/v1"`})
	}
	if !docutil.IsValidULID(r.RecordID) {
		Collect(&issues, &Issue{Path: []string{"record_id"}, Message: "Invalid"})
	}
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	Collect(&issues, RequiredString(r.PrID, 1, []string{"pr_id"}))
	Collect(&issues, RequiredString(r.Repo, 1, []string{"repo"}))
	if !r.ShadowOnly {
		Collect(&issues, &Issue{Path: []string{"shadow_only"}, Message: `Invalid literal value, expected true`})
	}
	if r.SkipReason != nil {
		Collect(&issues, RequiredString(*r.SkipReason, 1, []string{"skip_reason"}))
	}
	for _, iss := range r.Sampling.Validate() {
		iss.Path = append([]string{"sampling"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range r.Policy.Validate() {
		iss.Path = append([]string{"policy"}, iss.Path...)
		issues = append(issues, iss)
	}
	if r.Decision != nil {
		for _, iss := range r.Decision.Validate() {
			iss.Path = append([]string{"decision"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if r.InputHashes != nil {
		for _, iss := range r.InputHashes.Validate() {
			iss.Path = append([]string{"input_hashes"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	Collect(&issues, datetimeIssue(r.CreatedAt, []string{"created_at"}))
	Collect(&issues, datetimeIssue(r.ExpiresAt, []string{"expires_at"}))

	// `decision` + `input_hashes` are REQUIRED exactly when `sampled` is true
	// (fail closed): a sampled PR without its recorded shadow decision cannot
	// exist, and an unsampled record carries none.
	if r.Sampled && r.Decision == nil {
		Collect(&issues, &Issue{
			Path:    []string{"decision"},
			Message: "a sampled shadow record must carry the recorded shadow decision",
		})
	}
	if !r.Sampled && r.Decision != nil {
		Collect(&issues, &Issue{
			Path:    []string{"decision"},
			Message: "an unsampled shadow record must not carry a decision",
		})
	}
	if r.Sampled && r.InputHashes == nil {
		Collect(&issues, &Issue{
			Path:    []string{"input_hashes"},
			Message: "a sampled shadow record must record its input hashes (V-P12-2 lineage)",
		})
	}
	if !r.Sampled && r.InputHashes != nil {
		Collect(&issues, &Issue{
			Path:    []string{"input_hashes"},
			Message: "an unsampled shadow record must not carry input hashes",
		})
	}
	if !r.Sampled && r.SkipReason == nil {
		Collect(&issues, &Issue{
			Path:    []string{"skip_reason"},
			Message: "an unsampled shadow record must name its skip reason",
		})
	}
	if r.Sampled && r.SkipReason != nil {
		Collect(&issues, &Issue{
			Path:    []string{"skip_reason"},
			Message: "a sampled shadow record must not carry a skip reason",
		})
	}
	if created, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err == nil {
		if expires, err := time.Parse(time.RFC3339Nano, r.ExpiresAt); err == nil {
			if !expires.After(created) {
				Collect(&issues, &Issue{
					Path:    []string{"expires_at"},
					Message: "expires_at must be strictly after created_at (time-boxed canary)",
				})
			}
		}
	}
	return issues
}

// ParseShadowRunRecord strictly decodes and validates
// `docsync.shadow-run-record/v1` bytes (unknown JSON keys are rejected,
// mirroring z.strictObject).
func ParseShadowRunRecord(data []byte) (*ShadowRunRecord, error) {
	var r ShadowRunRecord
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// ShadowRunRecordExample mirrors SHADOW_RUN_RECORD_EXAMPLE (golden data for
// byte-for-byte serializer checks).
func ShadowRunRecordExample() ShadowRunRecord {
	return ShadowRunRecord{
		Schema:     "docsync.shadow-run-record/v1",
		RecordID:   "01JBSHAD0WBEHESTXAMP000000",
		RunID:      "shadow-v18-canary-01",
		PrID:       "8a73f2c",
		Repo:       "acme/widgets",
		ShadowOnly: true,
		Sampled:    true,
		Eligible:   true,
		Sampling: ShadowSampling{
			Sampler:      ShadowSamplerSHA256HexThreshold,
			Fraction:     0.1,
			DrawHex:      "00000001",
			ThresholdHex: "1999999a",
		},
		Policy:   ShadowRecordedPolicy{ProductionVersion: 17, CandidateVersion: 18},
		Decision: &ShadowDecision{Action: ActionReview, Abstained: false},
		InputHashes: &ShadowRecordedInputHashes{
			ChangeAnalysis: shadowExampleInputHash,
			CandidateSet:   shadowExampleInputHash,
			DiffSubset:     shadowExampleInputHash,
		},
		CreatedAt: "2026-10-09T00:00:00.000Z",
		ExpiresAt: "2026-10-23T00:00:00.000Z",
	}
}
