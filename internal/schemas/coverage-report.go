// Ported from packages/schemas/src/v1/coverage-report.ts: behavioral-proxy
// coverage report — `docsync.behavioral-proxy-coverage-report/v1` (P11-T22;
// ARCH §16.1 A5; D14).
//
// The A5 gate artifact: the per-language coverage audit measuring how well
// DETERMINISTIC behavioral proxies (signature / type / visibility /
// API-surface / config deltas — ARCH §16.1 A5: "externally observable
// behavior changed" is deterministically approximable by
// signature/type/API-surface proxies) detect behaviorally relevant changes
// over a labeled fixture corpus. The audit MEASURES the approximation
// quality; per-language recall gaps are DECLARED in the report, never silent,
// and uncertain cases route REVIEW (D14 — analyzer failure or ambiguity
// degrades toward less automation, never more).
//
// Verdict provenance mirrors the A4 trial convention (extractor/trial.ts):
// a verdict always carries its `data_source` — 'fixture' means the verdict
// was computed on labeled fixture data (a pilot study); it is NOT repository
// evidence until re-run over a real repository corpus.
//
// Pure query artifact — advisory evidence, never a state record. Zero LLM.
// This module shapes data; core computes the metrics deterministically.
package schemas

// BehavioralProxyCoverageReportSchemaID is the wire literal carried by the
// `schema` field (BEHAVIORAL_PROXY_COVERAGE_REPORT_SCHEMA_ID,
// z.literal('docsync.behavioral-proxy-coverage-report/v1')).
const BehavioralProxyCoverageReportSchemaID = "docsync.behavioral-proxy-coverage-report/v1"

// ProxyKind is the deterministic behavioral proxy auditable by this report
// (PROXY_KIND_VALUES):
//   - `signature`   — declaration signature changed (symbol-diff proxy).
//   - `type`        — written type annotations changed (symbol-diff proxy).
//   - `visibility`  — exported flag flipped (symbol-diff proxy).
//   - `api-surface` — exported symbols added/removed or an OpenAPI delta
//     classified by the W2 classifier (public surface proxies).
//   - `config`      — key-level resolved-value config diff (config proxy).
type ProxyKind string

// The proxy kinds in canonical order (PROXY_KIND_VALUES).
const (
	ProxyKindSignature  ProxyKind = "signature"
	ProxyKindType       ProxyKind = "type"
	ProxyKindVisibility ProxyKind = "visibility"
	ProxyKindAPISurface ProxyKind = "api-surface"
	ProxyKindConfig     ProxyKind = "config"
)

// ProxyKindValues lists every proxy kind in canonical order
// (PROXY_KIND_VALUES).
var ProxyKindValues = []ProxyKind{
	ProxyKindSignature, ProxyKindType, ProxyKindVisibility, ProxyKindAPISurface, ProxyKindConfig,
}

// IsValidProxyKind reports membership in PROXY_KIND.
func IsValidProxyKind(k string) bool {
	for _, v := range ProxyKindValues {
		if string(v) == k {
			return true
		}
	}
	return false
}

// BehavioralProxyTruth is the deterministic ground-truth label for a corpus
// case (BEHAVIORAL_PROXY_TRUTH_VALUES) — the fixture DECLARES its own truth
// (deterministic labeling, no model judgment):
//   - `behavioral`     — externally observable behavior changed.
//   - `non-behavioral` — behavior provably unchanged (e.g. comment/formatting
//     only).
//   - `uncertain`      — behavioral relevance is NOT determinable from the
//     fixture (e.g. a rename) — routes REVIEW (D14), never scored as a
//     hit/miss.
type BehavioralProxyTruth string

// The truth labels in canonical order (BEHAVIORAL_PROXY_TRUTH_VALUES).
const (
	ProxyTruthBehavioral    BehavioralProxyTruth = "behavioral"
	ProxyTruthNonBehavioral BehavioralProxyTruth = "non-behavioral"
	ProxyTruthUncertain     BehavioralProxyTruth = "uncertain"
)

// BehavioralProxyTruthValues lists every truth label in canonical order
// (BEHAVIORAL_PROXY_TRUTH_VALUES).
var BehavioralProxyTruthValues = []BehavioralProxyTruth{
	ProxyTruthBehavioral, ProxyTruthNonBehavioral, ProxyTruthUncertain,
}

// IsValidBehavioralProxyTruth reports membership in BEHAVIORAL_PROXY_TRUTH.
func IsValidBehavioralProxyTruth(t string) bool {
	for _, v := range BehavioralProxyTruthValues {
		if string(v) == t {
			return true
		}
	}
	return false
}

// AuditOutcome is the per-case audit outcome — deterministic from
// truth × proxy-fired (AUDIT_OUTCOME_VALUES):
//   - `proxy-hit`           — behavioral truth AND a proxy fired.
//   - `proxy-miss`          — behavioral truth, NO proxy fired → a DECLARED
//     recall gap (never silent — D14).
//   - `false-positive`      — non-behavioral truth but a proxy fired.
//   - `true-non-behavioral` — non-behavioral truth and no proxy fired.
//   - `review`              — uncertain truth → routed REVIEW (D14), excluded
//     from recall/false-positive math by construction.
type AuditOutcome string

// The audit outcomes in canonical order (AUDIT_OUTCOME_VALUES).
const (
	AuditOutcomeProxyHit          AuditOutcome = "proxy-hit"
	AuditOutcomeProxyMiss         AuditOutcome = "proxy-miss"
	AuditOutcomeFalsePositive     AuditOutcome = "false-positive"
	AuditOutcomeTrueNonBehavioral AuditOutcome = "true-non-behavioral"
	AuditOutcomeReview            AuditOutcome = "review"
)

// AuditOutcomeValues lists every audit outcome in canonical order
// (AUDIT_OUTCOME_VALUES).
var AuditOutcomeValues = []AuditOutcome{
	AuditOutcomeProxyHit, AuditOutcomeProxyMiss, AuditOutcomeFalsePositive,
	AuditOutcomeTrueNonBehavioral, AuditOutcomeReview,
}

// IsValidAuditOutcome reports membership in AUDIT_OUTCOME.
func IsValidAuditOutcome(o string) bool {
	for _, v := range AuditOutcomeValues {
		if string(v) == o {
			return true
		}
	}
	return false
}

// AuditVerdict is the overall audit verdict — a deterministic rule, see
// core's audit engine (AUDIT_VERDICT_VALUES).
type AuditVerdict string

// The audit verdicts in canonical order (AUDIT_VERDICT_VALUES).
const (
	AuditVerdictPass                 AuditVerdict = "pass"
	AuditVerdictPassWithDeclaredGaps AuditVerdict = "pass-with-declared-gaps"
	AuditVerdictFail                 AuditVerdict = "fail"
)

// AuditVerdictValues lists every audit verdict in canonical order
// (AUDIT_VERDICT_VALUES).
var AuditVerdictValues = []AuditVerdict{
	AuditVerdictPass, AuditVerdictPassWithDeclaredGaps, AuditVerdictFail,
}

// IsValidAuditVerdict reports membership in AUDIT_VERDICT.
func IsValidAuditVerdict(v string) bool {
	for _, x := range AuditVerdictValues {
		if string(x) == v {
			return true
		}
	}
	return false
}

// AuditDataSource is the provenance of the corpus the verdict was computed
// over (AUDIT_DATA_SOURCE_VALUES; A4 trial convention).
type AuditDataSource string

// The data sources in canonical order (AUDIT_DATA_SOURCE_VALUES).
const (
	AuditDataSourceFixture    AuditDataSource = "fixture"
	AuditDataSourceRepository AuditDataSource = "repository"
)

// AuditDataSourceValues lists every data source in canonical order
// (AUDIT_DATA_SOURCE_VALUES).
var AuditDataSourceValues = []AuditDataSource{AuditDataSourceFixture, AuditDataSourceRepository}

// IsValidAuditDataSource reports membership in AUDIT_DATA_SOURCE.
func IsValidAuditDataSource(s string) bool {
	for _, v := range AuditDataSourceValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// AuditDeclaredGap is one declared recall gap — a behaviorally-relevant case
// no proxy detected (AUDIT_DECLARED_GAP_SCHEMA).
type AuditDeclaredGap struct {
	CaseID string `json:"case_id"`
	Detail string `json:"detail"`
}

// Validate checks the gap against AUDIT_DECLARED_GAP_SCHEMA (strict).
func (g *AuditDeclaredGap) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(g.CaseID, 1, []string{"case_id"}))
	Collect(&issues, RequiredString(g.Detail, 1, []string{"detail"}))
	return issues
}

// AuditCaseRecord is one audited corpus case with its deterministic outcome
// (AUDIT_CASE_RECORD_SCHEMA; full evidence).
type AuditCaseRecord struct {
	CaseID       string               `json:"case_id"`
	Language     string               `json:"language"`
	Truth        BehavioralProxyTruth `json:"truth"`
	ProxyFired   bool                 `json:"proxy_fired"`
	FiredProxies []ProxyKind          `json:"fired_proxies"`
	Outcome      AuditOutcome         `json:"outcome"`
	ReviewRouted bool                 `json:"review_routed"`
}

// Validate checks the case against AUDIT_CASE_RECORD_SCHEMA (strict).
func (c *AuditCaseRecord) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.CaseID, 1, []string{"case_id"}))
	Collect(&issues, RequiredString(c.Language, 1, []string{"language"}))
	Collect(&issues, CheckEnum(string(c.Truth), stringsOf(BehavioralProxyTruthValues), []string{"truth"}))
	for i, p := range c.FiredProxies {
		Collect(&issues, CheckEnum(string(p), stringsOf(ProxyKindValues), []string{"fired_proxies", itoa(i)}))
	}
	Collect(&issues, CheckEnum(string(c.Outcome), stringsOf(AuditOutcomeValues), []string{"outcome"}))
	return issues
}

// AuditLanguageRow is the per-language coverage row — the A5 "per-language
// AST coverage numbers" (AUDIT_LANGUAGE_ROW_SCHEMA).
type AuditLanguageRow struct {
	Language              string             `json:"language"`
	AnalyzedCases         int                `json:"analyzed_cases"`
	BehaviorRelevantCases int                `json:"behavior_relevant_cases"`
	ProxyHits             int                `json:"proxy_hits"`
	Recall                float64            `json:"recall"`
	FalsePositives        int                `json:"false_positives"`
	FalsePositiveRate     float64            `json:"false_positive_rate"`
	DeclaredGaps          []AuditDeclaredGap `json:"declared_gaps"`
	ReviewRouted          []string           `json:"review_routed"`
}

// Validate checks the row against AUDIT_LANGUAGE_ROW_SCHEMA (strict).
func (r *AuditLanguageRow) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(r.Language, 1, []string{"language"}))
	if r.AnalyzedCases < 0 {
		Collect(&issues, &Issue{Path: []string{"analyzed_cases"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.BehaviorRelevantCases < 0 {
		Collect(&issues, &Issue{Path: []string{"behavior_relevant_cases"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.ProxyHits < 0 {
		Collect(&issues, &Issue{Path: []string{"proxy_hits"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.Recall < 0 {
		Collect(&issues, &Issue{Path: []string{"recall"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.Recall > 1 {
		Collect(&issues, &Issue{Path: []string{"recall"}, Message: "Number must be less than or equal to 1"})
	}
	if r.FalsePositives < 0 {
		Collect(&issues, &Issue{Path: []string{"false_positives"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.FalsePositiveRate < 0 {
		Collect(&issues, &Issue{Path: []string{"false_positive_rate"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.FalsePositiveRate > 1 {
		Collect(&issues, &Issue{Path: []string{"false_positive_rate"}, Message: "Number must be less than or equal to 1"})
	}
	for i := range r.DeclaredGaps {
		for _, iss := range r.DeclaredGaps[i].Validate() {
			iss.Path = append([]string{"declared_gaps", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i, c := range r.ReviewRouted {
		Collect(&issues, RequiredString(c, 1, []string{"review_routed", itoa(i)}))
	}
	return issues
}

// AuditSummary is the corpus-wide counters (AUDIT_SUMMARY_SCHEMA).
type AuditSummary struct {
	Cases            int `json:"cases"`
	BehaviorRelevant int `json:"behavior_relevant"`
	ProxyHits        int `json:"proxy_hits"`
	FalsePositives   int `json:"false_positives"`
	DeclaredGaps     int `json:"declared_gaps"`
	ReviewRouted     int `json:"review_routed"`
}

// Validate checks the summary against AUDIT_SUMMARY_SCHEMA (strict).
func (s *AuditSummary) Validate() []Issue {
	var issues []Issue
	if s.Cases < 0 {
		Collect(&issues, &Issue{Path: []string{"cases"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.BehaviorRelevant < 0 {
		Collect(&issues, &Issue{Path: []string{"behavior_relevant"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.ProxyHits < 0 {
		Collect(&issues, &Issue{Path: []string{"proxy_hits"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.FalsePositives < 0 {
		Collect(&issues, &Issue{Path: []string{"false_positives"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.DeclaredGaps < 0 {
		Collect(&issues, &Issue{Path: []string{"declared_gaps"}, Message: "Number must be greater than or equal to 0"})
	}
	if s.ReviewRouted < 0 {
		Collect(&issues, &Issue{Path: []string{"review_routed"}, Message: "Number must be greater than or equal to 0"})
	}
	return issues
}

// BehavioralProxyCoverageReport is the
// `docsync.behavioral-proxy-coverage-report/v1` A5 gate artifact
// (BEHAVIORAL_PROXY_COVERAGE_REPORT_SCHEMA).
type BehavioralProxyCoverageReport struct {
	Schema        string             `json:"schema"`
	CorpusID      string             `json:"corpus_id"`
	At            string             `json:"at"`
	RecallFloor   float64            `json:"recall_floor"`
	DataSource    AuditDataSource    `json:"data_source"`
	Languages     []AuditLanguageRow `json:"languages"`
	Cases         []AuditCaseRecord  `json:"cases"`
	Summary       AuditSummary       `json:"summary"`
	Verdict       AuditVerdict       `json:"verdict"`
	VerdictReason string             `json:"verdict_reason"`
}

// Validate checks the report against BEHAVIORAL_PROXY_COVERAGE_REPORT_SCHEMA
// (strict), reproducing the zod issue paths.
func (r *BehavioralProxyCoverageReport) Validate() []Issue {
	var issues []Issue
	if r.Schema != BehavioralProxyCoverageReportSchemaID {
		msg := `Invalid literal value, expected "` + BehavioralProxyCoverageReportSchemaID + `"`
		if r.Schema == "" {
			msg = "Required"
		}
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: msg})
	}
	Collect(&issues, RequiredString(r.CorpusID, 1, []string{"corpus_id"}))
	Collect(&issues, datetimeIssue(r.At, []string{"at"}))
	if r.RecallFloor < 0 {
		Collect(&issues, &Issue{Path: []string{"recall_floor"}, Message: "Number must be greater than or equal to 0"})
	}
	if r.RecallFloor > 1 {
		Collect(&issues, &Issue{Path: []string{"recall_floor"}, Message: "Number must be less than or equal to 1"})
	}
	Collect(&issues, CheckEnum(string(r.DataSource), stringsOf(AuditDataSourceValues), []string{"data_source"}))
	for i := range r.Languages {
		for _, iss := range r.Languages[i].Validate() {
			iss.Path = append([]string{"languages", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range r.Cases {
		for _, iss := range r.Cases[i].Validate() {
			iss.Path = append([]string{"cases", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range r.Summary.Validate() {
		iss.Path = append([]string{"summary"}, iss.Path...)
		issues = append(issues, iss)
	}
	Collect(&issues, CheckEnum(string(r.Verdict), stringsOf(AuditVerdictValues), []string{"verdict"}))
	Collect(&issues, RequiredString(r.VerdictReason, 1, []string{"verdict_reason"}))
	return issues
}

// ParseBehavioralProxyCoverageReport strictly parses and validates a
// `docsync.behavioral-proxy-coverage-report/v1` document, mirroring the zod
// strictObject contract (unknown keys rejected; absent required root keys
// rejected with zod's "Required").
func ParseBehavioralProxyCoverageReport(data []byte) (*BehavioralProxyCoverageReport, error) {
	var r BehavioralProxyCoverageReport
	if err := StrictDecode(data, &r); err != nil {
		return nil, err
	}
	if issues := requireRootKeys(data,
		"schema", "corpus_id", "at", "recall_floor", "data_source", "languages",
		"cases", "summary", "verdict", "verdict_reason"); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	if issues := r.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &r, nil
}

// BehavioralProxyCoverageReportExample is the deterministic example
// (BEHAVIORAL_PROXY_COVERAGE_REPORT_EXAMPLE).
var BehavioralProxyCoverageReportExample = BehavioralProxyCoverageReport{
	Schema:      BehavioralProxyCoverageReportSchemaID,
	CorpusID:    "fixtures/coverage/a5-pilot",
	At:          "2026-10-09T00:00:00Z",
	RecallFloor: 0.5,
	DataSource:  AuditDataSourceFixture,
	Languages: []AuditLanguageRow{
		{
			Language:              "typescript",
			AnalyzedCases:         3,
			BehaviorRelevantCases: 2,
			ProxyHits:             2,
			Recall:                1,
			FalsePositives:        0,
			FalsePositiveRate:     0,
			DeclaredGaps:          []AuditDeclaredGap{},
			ReviewRouted:          []string{},
		},
		{
			Language:              "yaml",
			AnalyzedCases:         1,
			BehaviorRelevantCases: 1,
			ProxyHits:             1,
			Recall:                1,
			FalsePositives:        0,
			FalsePositiveRate:     0,
			DeclaredGaps:          []AuditDeclaredGap{},
			ReviewRouted:          []string{},
		},
	},
	Cases: []AuditCaseRecord{
		{
			CaseID:       "case_signature",
			Language:     "typescript",
			Truth:        ProxyTruthBehavioral,
			ProxyFired:   true,
			FiredProxies: []ProxyKind{ProxyKindSignature},
			Outcome:      AuditOutcomeProxyHit,
			ReviewRouted: false,
		},
		{
			CaseID:       "case_type",
			Language:     "typescript",
			Truth:        ProxyTruthBehavioral,
			ProxyFired:   true,
			FiredProxies: []ProxyKind{ProxyKindType},
			Outcome:      AuditOutcomeProxyHit,
			ReviewRouted: false,
		},
		{
			CaseID:       "case_comment_only",
			Language:     "typescript",
			Truth:        ProxyTruthNonBehavioral,
			ProxyFired:   false,
			FiredProxies: []ProxyKind{},
			Outcome:      AuditOutcomeTrueNonBehavioral,
			ReviewRouted: false,
		},
		{
			CaseID:       "case_config_key",
			Language:     "yaml",
			Truth:        ProxyTruthBehavioral,
			ProxyFired:   true,
			FiredProxies: []ProxyKind{ProxyKindConfig},
			Outcome:      AuditOutcomeProxyHit,
			ReviewRouted: false,
		},
	},
	Summary: AuditSummary{
		Cases:            4,
		BehaviorRelevant: 3,
		ProxyHits:        3,
		FalsePositives:   0,
		DeclaredGaps:     0,
		ReviewRouted:     0,
	},
	Verdict:       AuditVerdictPass,
	VerdictReason: "all language recalls (min 1) meet the recall floor 0.5 with no declared gaps",
}
