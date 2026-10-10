package schemas

import "time"

// Ported from packages/schemas/src/v1/replacement-evaluation.ts
// (P12-T20, promotion of the P12-T11 local report shape into the registry;
// ARCH §10.3; R2; D9). ARCH §10.3: "`models.yml` changes are treated like
// policy changes: any model swap is shadow/canary evaluated (same machinery
// as §8.6) before production" and "Any model that honors the stage's typed
// interface is admissible; this is also what makes shadow-evaluating a
// *replacement model* ... possible with the same canary machinery."
//
// This is the REPORT — the human-auditable summary of one replacement-model
// shadow evaluation: what was swapped (old/new binding, production/candidate
// policy versions), every case that ran under BOTH bindings with its paired
// typed decisions and V-P12-2 input-hash lineage, the agreement arithmetic,
// the caller-supplied minimum agreement threshold (versioned data — D11,
// never hard-coded here) and the resulting `pass` | `fail` verdict.
//
// Shape notes:
//   - The canary-shaped data the policy-side lifecycle consumes is NOT here:
//     a report carries the per-case results, while the machinery ingests
//     validated `docsync.shadow-run-record/v1` rows emitted alongside it.
//     This schema is therefore the review surface, not the lifecycle input.
//   - Fail-closed invariants (the TS superRefine, ported below): a swap
//     compares TWO bindings (production ≠ candidate version, old ≠ new
//     primary), the agreement block must be internally consistent AND
//     consistent with the recorded cases, the verdict must follow the
//     recorded rate against the recorded threshold, and the report is
//     time-boxed (`expires_at` strictly after `created_at`, mirroring the
//     canary/shadow time-box data).
//   - The evaluation workload IS the sample (deliberately selected cases),
//     so the emitted canary records carry {@link ReplacementShadowSampler}
//     with `fraction: 1` — the deterministic sampler id, identical to the
//     policy-side sampler.
//
// The paired typed decisions (ShadowDecision) and the V-P12-2 input-hash
// lineage block (ShadowRecordedInputHashes) are imported from
// shadow-run-record.ts (ported in shadow.go) — imported, never duplicated.

// ReplacementEvaluationReportSchemaID is the registry ID
// (REPLACEMENT_EVALUATION_REPORT_SCHEMA_ID).
const ReplacementEvaluationReportSchemaID = "docsync.routing-replacement-report/v1"

// ReplacementShadowSampler is the deterministic sampler id — identical to
// the policy-side sampler (REPLACEMENT_SHADOW_SAMPLER).
const ReplacementShadowSampler = "sha256-hex-threshold/v1"

// ReplacementModelBinding is the minimal binding surface an evaluation names
// (what was/would be asked for) — REPLACEMENT_MODEL_BINDING_SCHEMA. The TS
// collections are `readonly()`; Go slices are mutable, but the wire bytes
// and validation behavior are identical.
type ReplacementModelBinding struct {
	Primary   string   `json:"primary"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// Validate checks the binding against REPLACEMENT_MODEL_BINDING_SCHEMA
// (strict).
func (b *ReplacementModelBinding) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(b.Primary, 1, []string{"primary"}))
	for i, f := range b.Fallbacks {
		Collect(&issues, RequiredString(f, 1, []string{"fallbacks", itoa(i)}))
	}
	return issues
}

// ReplacementEvalCaseInput is one evaluation case the evaluation port must
// run under BOTH bindings (TS interface, no zod schema — no Validate).
type ReplacementEvalCaseInput struct {
	// CaseID becomes the shadow record's `pr_id`.
	CaseID string `json:"caseId"`
	// RiskBucket is which risk bucket the case exercises (V-P12-11:
	// high-risk must demonstrably route to the stronger model).
	RiskBucket RiskBucket `json:"riskBucket"`
}

// ReplacementEvaluationBinding is the report-side binding: the primary
// actually asked for plus its fallbacks
// (REPLACEMENT_EVALUATION_BINDING_SCHEMA — fallbacks required, not
// optional).
type ReplacementEvaluationBinding struct {
	Primary   string   `json:"primary"`
	Fallbacks []string `json:"fallbacks"`
}

// Validate checks the binding against REPLACEMENT_EVALUATION_BINDING_SCHEMA
// (strict).
func (b *ReplacementEvaluationBinding) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(b.Primary, 1, []string{"primary"}))
	for i, f := range b.Fallbacks {
		Collect(&issues, RequiredString(f, 1, []string{"fallbacks", itoa(i)}))
	}
	return issues
}

// ReplacementEvalBindings pairs the old (production) and new (candidate)
// bindings a report compares.
type ReplacementEvalBindings struct {
	Old ReplacementEvaluationBinding `json:"old"`
	New ReplacementEvaluationBinding `json:"new"`
}

// ReplacementEvalCase is one evaluated case inside the report — paired
// decisions on the typed stage outcome (REPLACEMENT_EVAL_CASE_SCHEMA).
type ReplacementEvalCase struct {
	CaseID      string                    `json:"caseId"`
	RiskBucket  RiskBucket                `json:"riskBucket"`
	OldOutcome  ShadowDecision            `json:"old_outcome"`
	NewOutcome  ShadowDecision            `json:"new_outcome"`
	Agreed      bool                      `json:"agreed"`
	InputHashes ShadowRecordedInputHashes `json:"input_hashes"`
}

// Validate checks the case against REPLACEMENT_EVAL_CASE_SCHEMA (strict),
// delegating the paired decision and input-hash blocks to their imported
// schemas (shadow-run-record.ts).
func (c *ReplacementEvalCase) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.CaseID, 1, []string{"caseId"}))
	Collect(&issues, CheckEnum(string(c.RiskBucket), stringsOf(RiskBuckets), []string{"riskBucket"}))
	for _, iss := range c.OldOutcome.Validate() {
		iss.Path = append([]string{"old_outcome"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range c.NewOutcome.Validate() {
		iss.Path = append([]string{"new_outcome"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range c.InputHashes.Validate() {
		iss.Path = append([]string{"input_hashes"}, iss.Path...)
		issues = append(issues, iss)
	}
	return issues
}

// ReplacementEvalPolicy carries the canary-compatible versioning: a model
// swap IS a policy change (R2).
type ReplacementEvalPolicy struct {
	ProductionVersion int `json:"production_version"`
	CandidateVersion  int `json:"candidate_version"`
}

// Validate checks the policy versions block (strict).
func (p *ReplacementEvalPolicy) Validate() []Issue {
	var issues []Issue
	if p.ProductionVersion <= 0 {
		Collect(&issues, &Issue{Path: []string{"production_version"}, Message: "Number must be greater than 0"})
	}
	if p.CandidateVersion <= 0 {
		Collect(&issues, &Issue{Path: []string{"candidate_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// ReplacementEvalAgreement is the agreement arithmetic block.
type ReplacementEvalAgreement struct {
	Total     int     `json:"total"`
	Agreed    int     `json:"agreed"`
	Disagreed int     `json:"disagreed"`
	Rate      float64 `json:"rate"`
}

// Validate checks the agreement block (strict).
func (a *ReplacementEvalAgreement) Validate() []Issue {
	var issues []Issue
	if a.Total < 0 {
		Collect(&issues, &Issue{Path: []string{"total"}, Message: "Number must be greater than or equal to 0"})
	}
	if a.Agreed < 0 {
		Collect(&issues, &Issue{Path: []string{"agreed"}, Message: "Number must be greater than or equal to 0"})
	}
	if a.Disagreed < 0 {
		Collect(&issues, &Issue{Path: []string{"disagreed"}, Message: "Number must be greater than or equal to 0"})
	}
	if a.Rate < 0 {
		Collect(&issues, &Issue{Path: []string{"rate"}, Message: "Number must be greater than or equal to 0"})
	} else if a.Rate > 1 {
		Collect(&issues, &Issue{Path: []string{"rate"}, Message: "Number must be less than or equal to 1"})
	}
	return issues
}

// ReplacementEvaluationReportKind is the report kind literal.
const ReplacementEvaluationReportKind = "replacement_model_shadow_evaluation"

// ReplacementEvaluationReport is the `docsync.routing-replacement-report/v1`
// report (REPLACEMENT_EVALUATION_REPORT_SCHEMA).
type ReplacementEvaluationReport struct {
	Schema string `json:"schema"`
	Kind   string `json:"kind"`
	Role   string `json:"role"`
	Repo   string `json:"repo"`
	RunID  string `json:"run_id"`
	// Bindings compares TWO bindings: the old (production) and the new
	// (candidate) primary actually asked for.
	Bindings ReplacementEvalBindings `json:"bindings"`
	// Policy carries the canary-compatible versioning (a model swap IS a
	// policy change — R2).
	Policy ReplacementEvalPolicy `json:"policy"`
	// Cases holds the evaluated cases — an evaluation with no cases
	// evaluates nothing (min 1).
	Cases []ReplacementEvalCase `json:"cases"`
	// Agreement is the agreement arithmetic over the recorded cases.
	Agreement ReplacementEvalAgreement `json:"agreement"`
	// MinAgreementRate is the caller-supplied minimum agreement threshold
	// (versioned data — D11, never hard-coded).
	MinAgreementRate float64 `json:"min_agreement_rate"`
	Verdict          string  `json:"verdict"`
	PortID           string  `json:"port_id"`
	// CreatedAt is the report creation instant.
	CreatedAt string `json:"created_at"`
	// ExpiresAt is created_at + time-box — the canary records emitted
	// alongside carry the same window.
	ExpiresAt string `json:"expires_at"`
}

// Validate checks the report against REPLACEMENT_EVALUATION_REPORT_SCHEMA
// (strict) and applies the fail-closed superRefine invariants. Refinement
// rules run only when the field-level checks pass, mirroring zod, where
// refinements do not run on an aborted parse.
func (r *ReplacementEvaluationReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != ReplacementEvaluationReportSchemaID {
		msg := `Invalid literal value, expected "` + ReplacementEvaluationReportSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	if r.Kind != ReplacementEvaluationReportKind {
		msg := `Invalid literal value, expected "` + ReplacementEvaluationReportKind + `"`
		if r.Kind == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"kind"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.Role, 1, []string{"role"}))
	Collect(&issues, RequiredString(r.Repo, 1, []string{"repo"}))
	Collect(&issues, RequiredString(r.RunID, 1, []string{"run_id"}))
	for _, iss := range r.Bindings.Old.Validate() {
		iss.Path = append([]string{"bindings", "old"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range r.Bindings.New.Validate() {
		iss.Path = append([]string{"bindings", "new"}, iss.Path...)
		issues = append(issues, iss)
	}
	for _, iss := range r.Policy.Validate() {
		iss.Path = append([]string{"policy"}, iss.Path...)
		issues = append(issues, iss)
	}
	if len(r.Cases) < 1 {
		Collect(&issues, &Issue{Path: []string{"cases"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range r.Cases {
		for _, iss := range r.Cases[i].Validate() {
			iss.Path = append([]string{"cases", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.Agreement.Validate() {
		iss.Path = append([]string{"agreement"}, iss.Path...)
		issues = append(issues, iss)
	}
	if r.MinAgreementRate < 0 {
		Collect(&issues, &Issue{Path: []string{"min_agreement_rate"}, Message: "Number must be greater than or equal to 0"})
	} else if r.MinAgreementRate > 1 {
		Collect(&issues, &Issue{Path: []string{"min_agreement_rate"}, Message: "Number must be less than or equal to 1"})
	}
	Collect(&issues, CheckEnum(r.Verdict, []string{"pass", "fail"}, []string{"verdict"}))
	Collect(&issues, RequiredString(r.PortID, 1, []string{"port_id"}))
	// z.string().datetime({ offset: false }) — 'Z' suffix only, pinned by
	// the shared run-record.go helper.
	Collect(&issues, datetimeIssue(r.CreatedAt, []string{"created_at"}))
	Collect(&issues, datetimeIssue(r.ExpiresAt, []string{"expires_at"}))
	if len(issues) > 0 {
		return issues
	}

	// Fail-closed invariants (TS superRefine order preserved).
	issue := func(path []string, message string) {
		Collect(&issues, &Issue{Path: path, Message: message})
	}
	if r.Bindings.Old.Primary == r.Bindings.New.Primary {
		issue([]string{"bindings", "new", "primary"},
			"a replacement evaluation compares TWO bindings — the old and new primaries must differ")
	}
	if r.Policy.ProductionVersion == r.Policy.CandidateVersion {
		issue([]string{"policy", "candidate_version"},
			"a replacement evaluation compares TWO policy versions — production and candidate must differ (R2)")
	}
	if r.Agreement.Agreed+r.Agreement.Disagreed != r.Agreement.Total {
		issue([]string{"agreement", "disagreed"},
			"the agreement block must be internally consistent — agreed + disagreed === total")
	}
	if len(r.Cases) != r.Agreement.Total {
		issue([]string{"agreement", "total"},
			"the agreement total must equal the number of recorded cases")
	}
	agreedCases := 0
	for i := range r.Cases {
		if r.Cases[i].Agreed {
			agreedCases++
		}
	}
	if agreedCases != r.Agreement.Agreed {
		issue([]string{"agreement", "agreed"},
			"the agreed count must equal the agreed cases in the report")
	}
	if r.Verdict == "pass" && r.Agreement.Rate < r.MinAgreementRate {
		issue([]string{"verdict"},
			"a 'pass' verdict requires an agreement rate at or above the minimum")
	}
	if r.Verdict == "fail" && r.Agreement.Rate >= r.MinAgreementRate {
		issue([]string{"verdict"},
			"a 'fail' verdict requires an agreement rate below the minimum")
	}
	createdAt, createdAtErr := time.Parse(time.RFC3339, r.CreatedAt)
	expiresAt, expiresAtErr := time.Parse(time.RFC3339, r.ExpiresAt)
	if createdAtErr == nil && expiresAtErr == nil && !expiresAt.After(createdAt) {
		issue([]string{"expires_at"},
			"expires_at must be strictly after created_at (time-boxed canary)")
	}
	return issues
}

// ParseReplacementEvaluationReport strictly decodes and validates
// `docsync.routing-replacement-report/v1` bytes (unknown JSON keys are
// rejected, mirroring z.strictObject).
func ParseReplacementEvaluationReport(data []byte) (*ReplacementEvaluationReport, error) {
	var r ReplacementEvaluationReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// ReplacementEvaluationReportExample mirrors
// REPLACEMENT_EVALUATION_REPORT_EXAMPLE: a passing replacement-model shadow
// evaluation for one case (golden data). The input hashes are the TS
// example's literal `0123456789abcdef` repeated to 64 hex chars.
var ReplacementEvaluationReportExample = ReplacementEvaluationReport{
	Schema: ReplacementEvaluationReportSchemaID,
	Kind:   ReplacementEvaluationReportKind,
	Role:   "patch-writer",
	Repo:   "acme/widgets",
	RunID:  "replacement-shadow-patch-writer-v3-v2",
	Bindings: ReplacementEvalBindings{
		Old: ReplacementEvaluationBinding{
			Primary:   "anthropic/claude-sonnet-4.5",
			Fallbacks: []string{"anthropic/claude-3.7-sonnet"},
		},
		New: ReplacementEvaluationBinding{
			Primary:   "openai/gpt-5.1",
			Fallbacks: []string{},
		},
	},
	Policy: ReplacementEvalPolicy{ProductionVersion: 2, CandidateVersion: 3},
	Cases: []ReplacementEvalCase{
		{
			CaseID:     "case-001",
			RiskBucket: RiskBucketHigh,
			OldOutcome: ShadowDecision{Action: ActionPatch, Abstained: false},
			NewOutcome: ShadowDecision{Action: ActionPatch, Abstained: false},
			Agreed:     true,
			InputHashes: ShadowRecordedInputHashes{
				ChangeAnalysis: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				CandidateSet:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				DiffSubset:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
	},
	Agreement:        ReplacementEvalAgreement{Total: 1, Agreed: 1, Disagreed: 0, Rate: 1},
	MinAgreementRate: 0.9,
	Verdict:          "pass",
	PortID:           "recorded-double/v1",
	CreatedAt:        "2026-10-09T00:00:00.000Z",
	ExpiresAt:        "2026-10-23T00:00:00.000Z",
}
