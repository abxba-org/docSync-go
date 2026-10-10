package schemas

// Ported from packages/schemas/src/registry.ts (SCHEMA_REGISTRY + getSchema +
// SUPPORTED_MANIFEST_VERSIONS) and the *_EXAMPLE literals of
// packages/schemas/src/v1/*.ts that the Go package did not yet carry.
//
// This file owns the closed registry set (OD-R03): one entry per docSync
// artifact, in the TypeScript SCHEMA_REGISTRY declaration order, with the
// identical id, version (1), description and representative example, plus the
// Validate/Parse/ParseTyped wiring. Writers fail closed on schema drift
// against this registry exactly as the TS registry validation did.
//
// Example literals already exported by the Go package (DecisionExample,
// ChangeAnalysisExample, DocPatchExample, JudgeVerdictExample,
// TriggerMapExample, ClaimProposalExample, DriftReportExample,
// ClaimSupportReportExample, DriftFindingExample,
// DriftAuditRunRecordExample, BehavioralProxyCoverageReportExample,
// OrgPolicyExample, OrgDriftReportExample, RoutingPolicyExample,
// ReplacementEvaluationReportExample and the per-artifact
// <Type>Example() constructors) are referenced here, never redefined.
//
// The `state/` artifacts (Baseline, Claim, DocState, AuditCursor,
// PolicyPointer, ModelsConfig — state.ts) had no Go Parse entry points, so
// their strict parsers and the zod-faithful validators (validateBaseline,
// validateClaim, validateDocState, validateAuditCursor,
// validatePolicyPointer, validateModelsConfig) live here rather than in
// state.go, alongside the verification-report parser. This is the single file
// that owns registry wiring for the whole package.

// Wire formats are law: JSON keys stay snake_case exactly as zod declared
// them, so every example and parser below serializes identically to the
// TypeScript implementation.

import "sort"

/* --------------------------------------------------------- example literals */

// manifestExample mirrors MANIFEST_EXAMPLE (docsync.manifest v1).
var manifestExample = Manifest{
	Version: 1,
	Documents: []ManifestEntry{
		{
			Path:     "docs/payments.md",
			Mode:     modePtr(DocModeAuto),
			Sources:  []string{"src/payments/**"},
			Triggers: []string{"public_behavior", "api_change"},
			Ignore:   []string{"internal_refactor", "test_only"},
		},
		{
			Path:     "docs/architecture.md",
			Mode:     modePtr(DocModeSuggest),
			Sources:  []string{"src/**"},
			Triggers: []string{"architecture_change"},
		},
		{
			Path:    "docs/security.md",
			Mode:    modePtr(DocModeLocked),
			Sources: []string{"src/auth/**"},
		},
	},
}

// verificationReportExample mirrors VERIFICATION_REPORT_EXAMPLE (the
// three-tier "all green" verification report; the abstain twin stays in the
// verifier package's fixtures).
var verificationReportExample = VerificationReport{
	Schema:   "docsync.verification-report/v1",
	RunID:    "acme/payments#8a73f2c..81af92e",
	DocPath:  "docs/payments.md",
	PatchRef: "state/docs/payments.md#proposal-d_01J9ZK7Q2M",
	Tiers: []VerificationTierReport{
		{
			Tier:   1,
			Status: "pass",
			Checks: []VerificationCheck{
				{
					ID:     "patch-applies-cleanly",
					Status: "pass",
					Inputs: []string{
						"git:81af92e",
						"blob:9f2b7c1e4d3a5b6c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e",
					},
				},
				{ID: "manifest-allowlist-only", Status: "pass"},
				{ID: "ownership-floor", Status: "pass"},
				{ID: "minimality-bound", Status: "pass"},
				{ID: "whole-document-rewrite", Status: "pass"},
				{ID: "structural-integrity", Status: "pass"},
				{ID: "link-resolution", Status: "pass"},
				{ID: "examples-parse", Status: "pass"},
			},
		},
		{
			Tier:   2,
			Status: "pass",
			Checks: []VerificationCheck{
				{ID: "claim-decomposition", Status: "pass"},
				{
					ID:     "claim-anchor-binding",
					Status: "pass",
					Detail: strPtr("1 claim re-verified against src/payments/refund.ts:42"),
				},
				{ID: "head-verification", Status: "pass"},
				{ID: "over-deletion-guard", Status: "pass"},
				{ID: "contract-aware-check", Status: "pass"},
				{ID: "coverage-defect", Status: "pass"},
			},
		},
		{
			Tier:   3,
			Status: "pass",
			Checks: []VerificationCheck{
				{
					ID:     "judge-second-pass",
					Status: "pass",
					Detail: strPtr("stale_before=true, resolved_after=true, minimal=true"),
				},
				{ID: "judge-independence", Status: "pass"},
			},
		},
	},
	Claims:           []VerificationClaim{{ClaimID: "claim_182", Status: "supported"}},
	Verdict:          VerdictPass,
	AutoEligible:     true,
	AutoBlockReasons: []string{},
	PolicyVersion:    17,
	Judge: &VerificationJudge{
		Model:          "anthropic/claude-sonnet-4",
		RequestedModel: strPtr("anthropic/claude-sonnet-4"),
		Provider:       strPtr("anthropic"),
		Verdict:        VerdictPass,
		Questions: map[string]VerificationJudgeAnswer{
			"stale_before":              {Value: "true", Probability: 0.97},
			"resolved_after":            {Value: "true", Probability: 0.94},
			"minimal":                   {Value: "true", Probability: 0.98},
			"introduced_new_inaccuracy": {Value: "false", Probability: 0.96},
		},
		Reasons: []string{"all three tiers green; judge independently confirmed the drift is resolved"},
	},
}

// baselineExample mirrors BASELINE_EXAMPLE (state.ts).
var baselineExample = Baseline{
	DocPath:        "docs/payments.md",
	DocContentHash: "sha256:9f2b7c1e4d3a5b6c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e",
	BaselineCommit: "8a73f2c",
	SourceFiles:    []string{"src/payments/refund.ts", "src/payments/types.ts"},
	SourceSymbols:  []string{"refundRequest(idempotencyKey: string)"},
	ClaimIDs:       []string{"claim_182"},
	VerifiedAt:     "2026-10-04T22:31:00Z",
	VerifiedByRun:  "run_01J9ZK6X8A",
	PolicyVersion:  17,
}

// claimExample mirrors CLAIM_EXAMPLE (state.ts).
var claimExample = Claim{
	ID:   "claim_182",
	Text: "Refunds require an idempotency key.",
	Anchor: ClaimAnchor{
		File:        "docs/payments.md",
		StartLine:   42,
		EndLine:     42,
		ContentHash: "sha256:1f2e3d4c5b6a7988071627345a6b7c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7",
	},
	Source:  ClaimSource{File: "src/payments/refund.ts", Line: intPtr(42), BlobSHA: "e0d1fa5"},
	Concept: strPtr("refunds"),
	Status:  ClaimStatusVerified,
	History: []ClaimHistoryEvent{
		{At: "2026-10-04T22:31:00Z", RunID: "run_01J9ZK6X8A", Event: "verified"},
	},
}

// docStateExample mirrors DOC_STATE_EXAMPLE (state.ts).
var docStateExample = DocState{
	Path:          "docs/payments.md",
	State:         DocStateSuspect,
	Since:         "2026-10-04T22:30:44Z",
	LastRun:       strPtr("run_01J9ZK6X8A"),
	PolicyVersion: 17,
	EvidenceRefs:  []string{"analysis/01J9ZK6X8A"},
	Transitions: []DocStateTransition{
		{
			State:  DocStateSuspect,
			At:     "2026-10-04T22:30:44Z",
			RunID:  "run_01J9ZK6X8A",
			Reason: "mapped source files changed in 8a73f2c..81af92e",
		},
	},
}

// auditCursorExample mirrors AUDIT_CURSOR_EXAMPLE (state.ts).
var auditCursorExample = AuditCursor{
	Position:  "git:81af92e",
	UpdatedAt: "2026-10-04T22:31:00Z",
}

// policyPointerExample mirrors POLICY_EXAMPLE (state.ts; OD-R04).
var policyPointerExample = PolicyPointer{
	Schema:  "docsync.policy/v1",
	Version: 17,
	Pointer: "policy-history/v17.yml",
	Hash:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
}

// modelsConfigExample mirrors MODELS_EXAMPLE (state.ts; D9).
var modelsConfigExample = ModelsConfig{
	Schema:  "docsync.models/v1",
	Version: 1,
	Roles: map[string]ModelRoleBinding{
		"decision-engine": {Model: "z-ai/glm-4.7", Fallbacks: []string{"openai/gpt-4.1-mini"}},
		"judge":           {Model: "z-ai/glm-4.7"},
		"writer":          {Model: "anthropic/claude-sonnet-4.5"},
	},
}

// modePtr returns a pointer to m (local helper for the manifest example).
func modePtr(m DocMode) *DocMode { return &m }

/* ------------------------------------------------- state.go validators */

// validateBaseline checks b against BASELINE_SCHEMA (state.ts, strict).
func validateBaseline(b *Baseline) []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(b.DocPath, 1, []string{"doc_path"}))
	Collect(&issues, RequiredString(b.DocContentHash, 1, []string{"doc_content_hash"}))
	Collect(&issues, RequiredString(b.BaselineCommit, 1, []string{"baseline_commit"}))
	collectRequiredStrings(&issues, b.SourceFiles, []string{"source_files"})
	collectRequiredStrings(&issues, b.SourceSymbols, []string{"source_symbols"})
	collectRequiredStrings(&issues, b.ClaimIDs, []string{"claim_ids"})
	Collect(&issues, datetimeIssue(b.VerifiedAt, []string{"verified_at"}))
	Collect(&issues, RequiredString(b.VerifiedByRun, 1, []string{"verified_by_run"}))
	if b.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// validateClaim checks c against CLAIM_SCHEMA (state.ts, strict).
func validateClaim(c *Claim) []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.ID, 1, []string{"id"}))
	Collect(&issues, RequiredString(c.Text, 1, []string{"text"}))
	for _, iss := range c.Anchor.Validate() {
		iss.Path = append([]string{"anchor"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, RequiredString(c.Source.File, 1, []string{"source", "file"}))
	if c.Source.Line != nil && *c.Source.Line < 1 {
		Collect(&issues, &Issue{Path: []string{"source", "line"}, Message: "Number must be greater than or equal to 1"})
	}
	Collect(&issues, RequiredString(c.Source.BlobSHA, 1, []string{"source", "blob_sha"}))
	if c.Concept != nil {
		Collect(&issues, RequiredString(*c.Concept, 1, []string{"concept"}))
	}
	Collect(&issues, CheckEnum(c.Status, ClaimStatusValues, []string{"status"}))
	for i := range c.History {
		h := &c.History[i]
		Collect(&issues, datetimeIssue(h.At, []string{"history", itoa(i), "at"}))
		Collect(&issues, RequiredString(h.RunID, 1, []string{"history", itoa(i), "run_id"}))
		Collect(&issues, RequiredString(h.Event, 1, []string{"history", itoa(i), "event"}))
	}
	return issues
}

// validateDocState checks d against DOC_STATE_SCHEMA (state.ts, strict),
// including the append-only verification ledger and disagreement log.
func validateDocState(d *DocState) []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.Path, 1, []string{"path"}))
	Collect(&issues, CheckEnum(string(d.State), stringsOf(DocStateNameValues), []string{"state"}))
	Collect(&issues, datetimeIssue(d.Since, []string{"since"}))
	if d.LastRun != nil {
		Collect(&issues, RequiredString(*d.LastRun, 1, []string{"last_run"}))
	}
	if d.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	collectRequiredStrings(&issues, d.EvidenceRefs, []string{"evidence_refs"})
	for i := range d.Transitions {
		t := &d.Transitions[i]
		Collect(&issues, CheckEnum(string(t.State), stringsOf(DocStateNameValues), []string{"transitions", itoa(i), "state"}))
		Collect(&issues, datetimeIssue(t.At, []string{"transitions", itoa(i), "at"}))
		Collect(&issues, RequiredString(t.RunID, 1, []string{"transitions", itoa(i), "run_id"}))
		Collect(&issues, RequiredString(t.Reason, 1, []string{"transitions", itoa(i), "reason"}))
	}
	for i := range d.Verifications {
		v := &d.Verifications[i]
		if !IsBareSHA256Hex(v.ReportHash) {
			Collect(&issues, &Issue{
				Path:    []string{"verifications", itoa(i), "report_hash"},
				Message: "report_hash must be bare 64-hex sha256 (no sha256: prefix)",
			})
		}
		for _, iss := range v.Report.ValidateReport() {
			iss.Path = append([]string{"verifications", itoa(i), "report"}, iss.Path...)
			issues = append(issues, iss)
		}
		Collect(&issues, datetimeIssue(v.At, []string{"verifications", itoa(i), "at"}))
		Collect(&issues, RequiredString(v.RunID, 1, []string{"verifications", itoa(i), "run_id"}))
	}
	for i := range d.Disagreements {
		g := &d.Disagreements[i]
		Collect(&issues, datetimeIssue(g.At, []string{"disagreements", itoa(i), "at"}))
		Collect(&issues, RequiredString(g.RunID, 1, []string{"disagreements", itoa(i), "run_id"}))
		Collect(&issues, CheckEnum(string(g.VerifierVerdict), stringsOf(VerificationVerdictValues), []string{"disagreements", itoa(i), "verifier_verdict"}))
		Collect(&issues, CheckEnum(g.HumanOutcome, humanOutcomeValues, []string{"disagreements", itoa(i), "human_outcome"}))
		Collect(&issues, RequiredString(g.Reason, 1, []string{"disagreements", itoa(i), "reason"}))
		if g.PatchRef != nil {
			Collect(&issues, RequiredString(*g.PatchRef, 1, []string{"disagreements", itoa(i), "patch_ref"}))
		}
	}
	return issues
}

// validateAuditCursor checks c against AUDIT_CURSOR_SCHEMA (state.ts, strict).
func validateAuditCursor(c *AuditCursor) []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.Position, 1, []string{"position"}))
	Collect(&issues, datetimeIssue(c.UpdatedAt, []string{"updated_at"}))
	return issues
}

// validatePolicyPointer checks p against POLICY_SCHEMA (state.ts, strict).
func validatePolicyPointer(p *PolicyPointer) []Issue {
	var issues []Issue
	if p.Schema != "docsync.policy/v1" {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.policy/v1"`})
	}
	if p.Version < 1 {
		Collect(&issues, &Issue{Path: []string{"version"}, Message: "Number must be greater than 0"})
	}
	Collect(&issues, RequiredString(p.Pointer, 1, []string{"pointer"}))
	// z.string().min(1).optional(): an absent (empty) hash is valid; a present
	// one is covered by the type check at decode.
	return issues
}

// validateModelsConfig checks m against MODELS_SCHEMA (state.ts, strict).
func validateModelsConfig(m *ModelsConfig) []Issue {
	var issues []Issue
	if m.Schema != "docsync.models/v1" {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.models/v1"`})
	}
	if m.Version < 1 {
		Collect(&issues, &Issue{Path: []string{"version"}, Message: "Number must be greater than 0"})
	}
	roles := make([]string, 0, len(m.Roles))
	for role := range m.Roles {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		if len(role) < 1 {
			Collect(&issues, &Issue{Path: []string{"roles", role}, Message: "String must contain at least 1 character(s)"})
		}
		binding := m.Roles[role]
		Collect(&issues, RequiredString(binding.Model, 1, []string{"roles", role, "model"}))
		for i, f := range binding.Fallbacks {
			Collect(&issues, RequiredString(f, 1, []string{"roles", role, "fallbacks", itoa(i)}))
		}
	}
	return issues
}

// collectRequiredStrings validates z.array(z.string().min(1)) (no length
// floor), one issue per offending element, mirroring zod's per-element paths.
func collectRequiredStrings(issues *[]Issue, values []string, path []string) {
	for i, v := range values {
		elem := append(append([]string{}, path...), itoa(i))
		Collect(issues, RequiredString(v, 1, elem))
	}
}

/* -------------------------------------------- strict parsers (inline ports) */

// ParseVerificationReport strictly decodes and validates a verification
// report (VERIFICATION_REPORT_SCHEMA; unknown JSON keys are rejected,
// mirroring z.strictObject). The report's cross-field refinements live in
// ValidateReport.
func ParseVerificationReport(data []byte) (*VerificationReport, error) {
	var r VerificationReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := r.ValidateReport(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// ParseBaseline strictly decodes and validates `docsync.baseline/v1` JSON
// bytes (BASELINE_SCHEMA; unknown keys rejected).
func ParseBaseline(data []byte) (*Baseline, error) {
	var b Baseline
	if err := StrictDecode(data, &b); err != nil {
		return nil, err
	}
	if issues := validateBaseline(&b); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &b, nil
}

// ParseClaim strictly decodes and validates `docsync.claim/v1` JSON bytes
// (CLAIM_SCHEMA; unknown keys rejected).
func ParseClaim(data []byte) (*Claim, error) {
	var c Claim
	if err := StrictDecode(data, &c); err != nil {
		return nil, err
	}
	if issues := validateClaim(&c); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}

// ParseDocState strictly decodes and validates `docsync.doc-state/v1` JSON
// bytes (DOC_STATE_SCHEMA; unknown keys rejected).
func ParseDocState(data []byte) (*DocState, error) {
	var d DocState
	if err := StrictDecode(data, &d); err != nil {
		return nil, err
	}
	if issues := validateDocState(&d); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &d, nil
}

// ParseAuditCursor strictly decodes and validates `docsync.audit-cursor/v1`
// JSON bytes (AUDIT_CURSOR_SCHEMA; unknown keys rejected).
func ParseAuditCursor(data []byte) (*AuditCursor, error) {
	var c AuditCursor
	if err := StrictDecode(data, &c); err != nil {
		return nil, err
	}
	if issues := validateAuditCursor(&c); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}

// ParsePolicyPointer strictly decodes and validates `docsync.policy/v1`
// JSON bytes (POLICY_SCHEMA; unknown keys rejected).
func ParsePolicyPointer(data []byte) (*PolicyPointer, error) {
	var p PolicyPointer
	if err := StrictDecode(data, &p); err != nil {
		return nil, err
	}
	if issues := validatePolicyPointer(&p); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// ParseModelsConfig strictly decodes and validates `docsync.models/v1` JSON
// bytes (MODELS_SCHEMA; unknown keys rejected).
func ParseModelsConfig(data []byte) (*ModelsConfig, error) {
	var m ModelsConfig
	if err := StrictDecode(data, &m); err != nil {
		return nil, err
	}
	if issues := validateModelsConfig(&m); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &m, nil
}

/* ---------------------------------------------------------- registry wiring */

// The registry is the closed set of docSync artifact schemas (OD-R03). Entries
// are registered in the TypeScript SCHEMA_REGISTRY declaration order; the Go
// registry itself returns them sorted by id.
func init() {
	// docsync.manifest
	Register(RegisteredSchema{
		ID:          "docsync.manifest",
		Version:     1,
		Description: "Repository-scoped documentation manifest allowlist (ARCH §11.1).",
		Example:     manifestExample,
		Validate: func() []Issue {
			m := manifestExample
			return m.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseManifest(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseManifest(data) },
	})

	// docsync.decision
	Register(RegisteredSchema{
		ID:          "docsync.decision",
		Version:     1,
		Description: "Decision object on the wire produced by the decision-engine role (ARCH §5.4).",
		Example:     DecisionExample,
		Validate: func() []Issue {
			d := DecisionExample
			return d.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseDecision(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDecision(data) },
	})

	// docsync.change-analysis
	Register(RegisteredSchema{
		ID:          "docsync.change-analysis",
		Version:     1,
		Description: "Deterministic ChangeAnalysis produced before any AI stage (ARCH §4.1).",
		Example:     ChangeAnalysisExample,
		Validate: func() []Issue {
			a := ChangeAnalysisExample
			return a.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseChangeAnalysis(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseChangeAnalysis(data) },
	})

	// docsync.doc-patch
	Register(RegisteredSchema{
		ID:          "docsync.doc-patch",
		Version:     1,
		Description: "Structured minimal DocPatch — anchored hunks, never rewrites (ARCH §4.9).",
		Example:     DocPatchExample,
		Validate: func() []Issue {
			p := DocPatchExample
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseDocPatch(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDocPatch(data) },
	})

	// docsync.verification-report
	Register(RegisteredSchema{
		ID:          "docsync.verification-report",
		Version:     1,
		Description: "Three-tier verification report for a proposed patch (ARCH §4.8).",
		Example:     verificationReportExample,
		Validate: func() []Issue {
			r := verificationReportExample
			return r.ValidateReport()
		},
		Parse: func(data []byte) error {
			_, err := ParseVerificationReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseVerificationReport(data) },
	})

	// docsync.judge-verdict
	Register(RegisteredSchema{
		ID:          "docsync.judge-verdict",
		Version:     1,
		Description: "Schema-validated model response body of the Tier-3 judge role — the verification report judge block mirrors this body contract (ARCH §4.8; D13).",
		Example:     JudgeVerdictExample,
		Validate: func() []Issue {
			b := JudgeVerdictExample
			return b.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseJudgeVerdictBody(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseJudgeVerdictBody(data) },
	})

	// docsync.provenance
	Register(RegisteredSchema{
		ID:          "docsync.provenance",
		Version:     1,
		Description: "Structured provenance attached to every automated doc change (ARCH §12.5).",
		Example:     ProvenanceExample(),
		Validate: func() []Issue {
			p := ProvenanceExample()
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseProvenance(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseProvenance(data) },
	})

	// docsync.run-record
	Register(RegisteredSchema{
		ID:          "docsync.run-record",
		Version:     1,
		Description: "Run record with idempotency key and A8 state-churn counters (ARCH §3.5/§6.3).",
		Example:     RunRecordExample(),
		Validate: func() []Issue {
			r := RunRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseRunRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseRunRecord(data) },
	})

	// docsync.baseline
	Register(RegisteredSchema{
		ID:          "docsync.baseline",
		Version:     1,
		Description: "Verified documentation baseline record (ARCH §6.1).",
		Example:     baselineExample,
		Validate: func() []Issue {
			b := baselineExample
			return validateBaseline(&b)
		},
		Parse: func(data []byte) error {
			_, err := ParseBaseline(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseBaseline(data) },
	})

	// docsync.claim
	Register(RegisteredSchema{
		ID:          "docsync.claim",
		Version:     1,
		Description: "Atomic documented claim with content-hash anchor (ARCH §4.3/§6.3).",
		Example:     claimExample,
		Validate: func() []Issue {
			c := claimExample
			return validateClaim(&c)
		},
		Parse: func(data []byte) error {
			_, err := ParseClaim(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseClaim(data) },
	})

	// docsync.doc-state
	Register(RegisteredSchema{
		ID:          "docsync.doc-state",
		Version:     1,
		Description: "Per-document state-machine record with append-only transitions (ARCH §6.2).",
		Example:     docStateExample,
		Validate: func() []Issue {
			d := docStateExample
			return validateDocState(&d)
		},
		Parse: func(data []byte) error {
			_, err := ParseDocState(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDocState(data) },
	})

	// docsync.audit-cursor
	Register(RegisteredSchema{
		ID:          "docsync.audit-cursor",
		Version:     1,
		Description: "Loop B incremental audit cursor (ARCH §6.3).",
		Example:     auditCursorExample,
		Validate: func() []Issue {
			c := auditCursorExample
			return validateAuditCursor(&c)
		},
		Parse: func(data []byte) error {
			_, err := ParseAuditCursor(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseAuditCursor(data) },
	})

	// docsync.policy
	Register(RegisteredSchema{
		ID:          "docsync.policy",
		Version:     1,
		Description: "policy.yml live pointer into policy-history (OD-R04; ARCH §8.4).",
		Example:     policyPointerExample,
		Validate: func() []Issue {
			p := policyPointerExample
			return validatePolicyPointer(&p)
		},
		Parse: func(data []byte) error {
			_, err := ParsePolicyPointer(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParsePolicyPointer(data) },
	})

	// docsync.models
	Register(RegisteredSchema{
		ID:          "docsync.models",
		Version:     1,
		Description: "Role → OpenRouter model bindings (D9; ARCH §10).",
		Example:     modelsConfigExample,
		Validate: func() []Issue {
			m := modelsConfigExample
			return validateModelsConfig(&m)
		},
		Parse: func(data []byte) error {
			_, err := ParseModelsConfig(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseModelsConfig(data) },
	})

	// docsync.trigger-map
	Register(RegisteredSchema{
		ID:          "docsync.trigger-map",
		Version:     1,
		Description: "Versioned trigger-taxonomy mapping table (ARCH §8.3.1/§11.3).",
		Example:     TriggerMapExample,
		Validate: func() []Issue {
			t := TriggerMapExample
			return t.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseTriggerMap(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseTriggerMap(data) },
	})

	// docsync.concepts
	Register(RegisteredSchema{
		ID:          "docsync.concepts",
		Version:     1,
		Description: "Manifest-adjacent concepts.yml — concept membership, contract declarations and hand-anchored claims (OD-R36; ARCH §4.2–§4.4).",
		Example:     ConceptsExample(),
		Validate: func() []Issue {
			c := ConceptsExample()
			return c.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseConcepts(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseConcepts(data) },
	})

	// docsync.run-outcome
	Register(RegisteredSchema{
		ID:          "docsync.run-outcome",
		Version:     1,
		Description: "FROZEN single-run pipeline outcome consumed by the Phase 7 CLI and Phase 8 CI (PLAN §6.3; ARCH §12.1; V-P5-16).",
		Example:     RunOutcomeExample(),
		Validate: func() []Issue {
			r := RunOutcomeExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseRunOutcome(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseRunOutcome(data) },
	})

	// docsync.canonical-report
	Register(RegisteredSchema{
		ID:          "docsync.canonical-report",
		Version:     1,
		Description: "Canonical display report derived from the frozen run outcome — the §12.1 parity contract shared by the Phase 7 CLI and Phase 8 CI (P7-T01; ARCH §12.1).",
		Example:     CanonicalReportExample(),
		Validate: func() []Issue {
			r := CanonicalReportExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseCanonicalReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseCanonicalReport(data) },
	})

	// docsync.fix-run-record
	Register(RegisteredSchema{
		ID:          "docsync.fix-run-record",
		Version:     1,
		Description: "Interactive fix-run record - applied/skipped/refused lanes persisted via CAS for idempotent fix runs (P7-T09; ARCH 6.3).",
		Example:     FixRunRecordExample(),
		Validate: func() []Issue {
			r := FixRunRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseFixRunRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseFixRunRecord(data) },
	})

	// docsync.feedback-record
	Register(RegisteredSchema{
		ID:          "docsync.feedback-record",
		Version:     1,
		Description: "Post-merge lifecycle feedback signal on a doc PR — accepted/corrected/rejected evidence for the Phase 10 calibration loop (P6-T22; OD-R14).",
		Example:     FeedbackRecordExample(),
		Validate: func() []Issue {
			r := FeedbackRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseFeedbackRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseFeedbackRecord(data) },
	})

	// docsync.safety-incident
	Register(RegisteredSchema{
		ID:          "docsync.safety-incident",
		Version:     1,
		Description: "Record of a tripped safety invariant — append-only evidence of a fail-closed block (P6-T12; ARCH §14 invariants).",
		Example:     SafetyIncidentExample(),
		Validate: func() []Issue {
			s := SafetyIncidentExample()
			return s.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseSafetyIncident(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseSafetyIncident(data) },
	})

	// docsync.merge-ledger
	Register(RegisteredSchema{
		ID:          "docsync.merge-ledger",
		Version:     1,
		Description: "Per-source-key doc-PR lifecycle ledger for merge ordering and idempotent PR writes (P6-T18; no-flood invariant).",
		Example:     MergeLedgerExample(),
		Validate: func() []Issue {
			l := MergeLedgerExample()
			return l.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseMergeLedger(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseMergeLedger(data) },
	})

	// docsync.feedback-event
	Register(RegisteredSchema{
		ID:          "docsync.feedback-event",
		Version:     1,
		Description: "Raw reviewer signal on a decision — five-signal capture for the learning loop (P10-T01/T02; ARCH §8.7).",
		Example:     FeedbackEventExample(),
		Validate: func() []Issue {
			e := FeedbackEventExample()
			return e.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseFeedbackEvent(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseFeedbackEvent(data) },
	})

	// docsync.hygiene-label
	Register(RegisteredSchema{
		ID:          "docsync.hygiene-label",
		Version:     1,
		Description: "Hygiene-normalized evaluation label derived once from a feedback event before any counting (P10-T03; ARCH §8.7).",
		Example:     HygieneLabelExample(),
		Validate: func() []Issue {
			l := HygieneLabelExample()
			return l.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseHygieneLabel(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseHygieneLabel(data) },
	})

	// docsync.eval-case
	Register(RegisteredSchema{
		ID:          "docsync.eval-case",
		Version:     1,
		Description: "Labeled benchmark case over a run key — incl. true negatives — for offline evaluation (P10-T07; ARCH §9).",
		Example:     EvalCaseExample(),
		Validate: func() []Issue {
			c := EvalCaseExample()
			return c.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseEvalCase(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseEvalCase(data) },
	})

	// docsync.evaluation-report
	Register(RegisteredSchema{
		ID:          "docsync.evaluation-report",
		Version:     1,
		Description: "Per-policy-version six-metric evaluation report with abstain health and disagreement (P10-T10; ARCH §9).",
		Example:     EvaluationReportExample(),
		Validate: func() []Issue {
			r := EvaluationReportExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseEvaluationReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseEvaluationReport(data) },
	})

	// docsync.policy-proposal
	Register(RegisteredSchema{
		ID:          "docsync.policy-proposal",
		Version:     1,
		Description: "Policy Architect proposal artifact — counted evidence mandatory, eight types (P10-T14; ARCH §8.1).",
		Example:     PolicyProposalExample(),
		Validate: func() []Issue {
			p := PolicyProposalExample()
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParsePolicyProposal(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParsePolicyProposal(data) },
	})

	// docsync.memory-record
	Register(RegisteredSchema{
		ID:          "docsync.memory-record",
		Version:     1,
		Description: "Evidence-backed, prunable memory entry in the exactly-four-file layout (P10-T20; ARCH §8.8, D1).",
		Example:     MemoryRecordExample(),
		Validate: func() []Issue {
			r := MemoryRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseMemoryRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseMemoryRecord(data) },
	})

	// docsync.promotion-record
	Register(RegisteredSchema{
		ID:          "docsync.promotion-record",
		Version:     1,
		Description: "Versioned human-approved policy promotion with embedded gate evidence (P10-T18; ARCH §8.4, INV-7).",
		Example:     PromotionRecordExample(),
		Validate: func() []Issue {
			r := PromotionRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParsePromotionRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParsePromotionRecord(data) },
	})

	// docsync.claim-proposal
	Register(RegisteredSchema{
		ID:          "docsync.claim-proposal",
		Version:     1,
		Description: "Claim-extractor proposal artifact — quoted source evidence mandatory, verified only via the deterministic truth gate (P11-T12/T13; ARCH §4.3).",
		Example:     ClaimProposalExample,
		Validate: func() []Issue {
			p := ClaimProposalExample
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseClaimProposal(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseClaimProposal(data) },
	})

	// docsync.drift-report
	Register(RegisteredSchema{
		ID:          "docsync.drift-report",
		Version:     1,
		Description: "Baseline differential — HEAD vs baseline-commit drift intersected with the recorded baseline scope (P11-T16; ARCH §6.1; PRIN-2).",
		Example:     DriftReportExample,
		Validate: func() []Issue {
			r := DriftReportExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseDriftReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDriftReport(data) },
	})

	// docsync.claim-support-report
	Register(RegisteredSchema{
		ID:          "docsync.claim-support-report",
		Version:     1,
		Description: "Claim-support sweep report — the headline query \"which documented claims lost support in the source of truth?\", report-only by default with CAS-guarded state flips (P11-T17; ARCH §4.3).",
		Example:     ClaimSupportReportExample,
		Validate: func() []Issue {
			r := ClaimSupportReportExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseClaimSupportReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseClaimSupportReport(data) },
	})

	// docsync.drift-finding
	Register(RegisteredSchema{
		ID:          "docsync.drift-finding",
		Version:     1,
		Description: "Evidence-carrying background drift finding — claim IDs, commits, confidence; advisory data, never a write instruction (P11-T20; ARCH §7.2).",
		Example:     DriftFindingExample,
		Validate: func() []Issue {
			f := DriftFindingExample
			return f.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseDriftFindingRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDriftFindingRecord(data) },
	})

	// docsync.drift-audit-run-record
	Register(RegisteredSchema{
		ID:          "docsync.drift-audit-run-record",
		Version:     1,
		Description: "Loop B audit run provenance record — trigger, cursor positions, audited/skipped manifest docs, status and the deduplicated findings (\"provenance or it didn't happen\"; P11-T20; ARCH §7.2; D5).",
		Example:     DriftAuditRunRecordExample,
		Validate: func() []Issue {
			r := DriftAuditRunRecordExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseDriftAuditRunRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseDriftAuditRunRecord(data) },
	})

	// docsync.behavioral-proxy-coverage-report
	Register(RegisteredSchema{
		ID:          "docsync.behavioral-proxy-coverage-report",
		Version:     1,
		Description: "A5 behavioral-proxy coverage audit — per-language recall/false-positive metrics of the deterministic behavioral proxies over a labeled corpus, with declared recall gaps and REVIEW routing (P11-T22; ARCH §16.1 A5; D14).",
		Example:     BehavioralProxyCoverageReportExample,
		Validate: func() []Issue {
			r := BehavioralProxyCoverageReportExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseBehavioralProxyCoverageReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseBehavioralProxyCoverageReport(data) },
	})

	// docsync.shadow-agreement-matrix
	Register(RegisteredSchema{
		ID:          "docsync.shadow-agreement-matrix",
		Version:     1,
		Description: "Decision-level candidate-vs-production agreement/disagreement matrix with per-case identical-input hashes and reviewer-resolvable disagreement labels (P12-T04; ARCH §8.6; D6; V-P12-2/3).",
		Example:     ShadowAgreementMatrixExample(),
		Validate: func() []Issue {
			m := ShadowAgreementMatrixExample()
			return m.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseShadowAgreementMatrix(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseShadowAgreementMatrix(data) },
	})

	// docsync.shadow-run-record
	Register(RegisteredSchema{
		ID:          "docsync.shadow-run-record",
		Version:     1,
		Description: "Sampled live-shadow run record — deterministic sampling parameters, recorded shadow-only decision and input hashes, time-box expiry data; never surfaced to developers (P12-T02; ARCH §8.6/§7.1; D6; OD-R29).",
		Example:     ShadowRunRecordExample(),
		Validate: func() []Issue {
			r := ShadowRunRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseShadowRunRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseShadowRunRecord(data) },
	})

	// docsync.canary-record
	Register(RegisteredSchema{
		ID:          "docsync.canary-record",
		Version:     1,
		Description: "Time-boxed canary lifecycle row under `.docsync/policy-history/` — active/expired/promoted/rejected transitions with the shadow-run link and auto-expiry data; an expired canary is never consulted again and never promotable (P12-T05; ARCH §8.6; CTX §21).",
		Example:     CanaryRecordExample(),
		Validate: func() []Issue {
			r := CanaryRecordExample()
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseCanaryRecord(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseCanaryRecord(data) },
	})

	// docsync.threshold-calibration
	Register(RegisteredSchema{
		ID:          "docsync.threshold-calibration",
		Version:     1,
		Description: "Adaptive-threshold calibration artifact — per-policy-version calibrated routing thresholds (0.94/0.72-class data, D11) with the fixed confidence × risk matrix shape hash, evaluation evidence refs, time-split report and rolling re-evaluation report; a change only ever produces a NEW versioned artifact (P12-T08; ARCH §5.3/§10.8; R4; V-P12-10).",
		Example:     ThresholdCalibrationExample(),
		Validate: func() []Issue {
			c := ThresholdCalibrationExample()
			return c.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseThresholdCalibration(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseThresholdCalibration(data) },
	})

	// docsync.org-policy (ORG_POLICY_SCHEMA_ID)
	Register(RegisteredSchema{
		ID:          OrgPolicySchemaID,
		Version:     1,
		Description: "Organizational policy floors — mode ceilings and report-only-until-reviewed floors across repositories; fail-closed validation (D8), locked terminal (INV-4), precedence: flags > per-doc > manifest defaults > org floors > built-ins (P12-T13; ARCH §11.2, §11.3; D8; OD-R30).",
		Example:     OrgPolicyExample,
		Validate: func() []Issue {
			p := OrgPolicyExample
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseOrgPolicy(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseOrgPolicy(data) },
	})

	// docsync.org-drift-report (ORG_DRIFT_REPORT_SCHEMA_ID)
	Register(RegisteredSchema{
		ID:          OrgDriftReportSchemaID,
		Version:     1,
		Description: "Org-wide drift report — scheduled aggregation of background drift findings across repositories with cross-repo shared-concept (terminology/contract drift) surfaces; advisory-only by schema invariant (advisory: true literal), findings surface and never auto-write (P12-T15; ARCH §15.3; CTX §42; OD-R32; V-P12-14).",
		Example:     OrgDriftReportExample,
		Validate: func() []Issue {
			r := OrgDriftReportExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseOrgDriftReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseOrgDriftReport(data) },
	})

	// docsync.routing-policy (ROUTING_POLICY_SCHEMA_ID)
	Register(RegisteredSchema{
		ID:          RoutingPolicySchemaID,
		Version:     1,
		Description: "Versioned declarative org routing policy — the bucket → model mapping (\"high-risk cases → stronger model\", ARCH §10.1) as reviewable DATA, every bucket bound explicitly with no guessed default model (fail-closed); promotion of the local operational schema into the registry (P12-T20 (promotion of P12-T10/T11); ARCH §10.3; R2; D9).",
		Example:     RoutingPolicyExample,
		Validate: func() []Issue {
			p := RoutingPolicyExample
			return p.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseRoutingPolicy(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseRoutingPolicy(data) },
	})

	// docsync.replacement-evaluation-report (REPLACEMENT_EVALUATION_REPORT_SCHEMA_ID)
	Register(RegisteredSchema{
		ID:          ReplacementEvaluationReportSchemaID,
		Version:     1,
		Description: "Replacement-model shadow evaluation report — the human-auditable summary of one model swap shadow-evaluated through the same canary machinery (a model swap IS a policy change, R2): old/new bindings, production/candidate policy versions, per-case paired typed decisions with V-P12-2 input-hash lineage, agreement arithmetic, the caller-supplied minimum agreement rate (versioned data — D11) and the pass/fail verdict; promotion of the local operational report shape into the registry (P12-T20 (promotion of P12-T11); ARCH §10.3; R2; D9).",
		Example:     ReplacementEvaluationReportExample,
		Validate: func() []Issue {
			r := ReplacementEvaluationReportExample
			return r.Validate()
		},
		Parse: func(data []byte) error {
			_, err := ParseReplacementEvaluationReport(data)
			return err
		},
		ParseTyped: func(data []byte) (any, error) { return ParseReplacementEvaluationReport(data) },
	})
}
