// Ported from packages/schemas/src/v1/change-analysis.ts
package schemas

// ChangeAnalysis draft (ARCH §4.1/§3.3; Phase 1 consumes). Deterministic,
// cacheable by blob SHAs; produced before any AI stage. Analyzer failure is
// inconclusive, never negative (D14) — degradation is declared via
// `analyzer_coverage`.
//
// @draft — Phase 1 finalizes field-level detail (symbol/API/config deltas).

// ChangeAnalysisSchemaID is the wire literal carried by the `schema` field
// (z.literal('docsync.change-analysis/v1')).
const ChangeAnalysisSchemaID = "docsync.change-analysis/v1"

// FileStatus is one changed-file status (FILE_STATUS).
type FileStatus string

// File status values in canonical order (FILE_STATUS_VALUES).
const (
	FileStatusAdded    FileStatus = "added"
	FileStatusModified FileStatus = "modified"
	FileStatusDeleted  FileStatus = "deleted"
	FileStatusRenamed  FileStatus = "renamed"
)

// FileStatusValues lists every file status in canonical order.
var FileStatusValues = []FileStatus{FileStatusAdded, FileStatusModified, FileStatusDeleted, FileStatusRenamed}

// ClassificationHintKind is the classification-hint taxonomy
// (CLASSIFICATION_HINT_KIND).
type ClassificationHintKind string

// Classification hint kinds in canonical order (CLASSIFICATION_HINT_KIND_VALUES).
const (
	ClassificationHintTestOnly       ClassificationHintKind = "test_only"
	ClassificationHintFormattingOnly ClassificationHintKind = "formatting_only"
	ClassificationHintGenerated      ClassificationHintKind = "generated"
	ClassificationHintOther          ClassificationHintKind = "other"
)

// ClassificationHintKindValues lists every classification hint kind in
// canonical order.
var ClassificationHintKindValues = []ClassificationHintKind{
	ClassificationHintTestOnly, ClassificationHintFormattingOnly, ClassificationHintGenerated, ClassificationHintOther,
}

// DeepAnalysisStatus is the deep-analysis outcome (DEEP_ANALYSIS_STATUS).
type DeepAnalysisStatus string

// Deep analysis status values in canonical order (DEEP_ANALYSIS_STATUS_VALUES).
const (
	DeepAnalysisPerformed DeepAnalysisStatus = "performed"
	DeepAnalysisSkipped   DeepAnalysisStatus = "skipped"
)

// DeepAnalysisStatusValues lists every deep-analysis status in canonical order.
var DeepAnalysisStatusValues = []DeepAnalysisStatus{DeepAnalysisPerformed, DeepAnalysisSkipped}

// AnalyzerName identifies the analyzer that produced coverage
// (ANALYZER_NAME).
type AnalyzerName string

// Analyzer name values in canonical order (ANALYZER_NAME_VALUES).
const (
	AnalyzerNameTypeScript AnalyzerName = "typescript"
	AnalyzerNameGeneric    AnalyzerName = "generic"
)

// AnalyzerNameValues lists every analyzer name in canonical order.
var AnalyzerNameValues = []AnalyzerName{AnalyzerNameTypeScript, AnalyzerNameGeneric}

// AnalyzerStatus is the degradation state of one analyzer
// (ANALYZER_STATUS).
type AnalyzerStatus string

// Analyzer status values in canonical order (ANALYZER_STATUS_VALUES).
const (
	AnalyzerStatusOK          AnalyzerStatus = "ok"
	AnalyzerStatusUnavailable AnalyzerStatus = "unavailable"
	AnalyzerStatusStub        AnalyzerStatus = "stub"
)

// AnalyzerStatusValues lists every analyzer status in canonical order.
var AnalyzerStatusValues = []AnalyzerStatus{AnalyzerStatusOK, AnalyzerStatusUnavailable, AnalyzerStatusStub}

// SymbolChangeKind is one semantic dimension along which a paired symbol can
// differ (P11-T03 enrichment, E2; mirrors `@docsync/analyzer-typescript`
// `SymbolChangeKind` — this package cannot import the analyzer (dependency
// direction), keep in sync; analyzer run records catch drift).
type SymbolChangeKind string

// Symbol change kind values in canonical order (SYMBOL_CHANGE_KIND_VALUES).
const (
	SymbolChangeSignature  SymbolChangeKind = "signature"
	SymbolChangeType       SymbolChangeKind = "type"
	SymbolChangeVisibility SymbolChangeKind = "visibility"
)

// SymbolChangeKindValues lists every symbol change kind in canonical order.
var SymbolChangeKindValues = []SymbolChangeKind{SymbolChangeSignature, SymbolChangeType, SymbolChangeVisibility}

// SymbolChangeDetail is one changed symbol in the record-level symbol evidence
// projection (SYMBOL_CHANGE_DETAIL_SCHEMA).
type SymbolChangeDetail struct {
	Name        string             `json:"name"`
	ChangeKinds []SymbolChangeKind `json:"changeKinds"`
}

// Validate checks the detail against SYMBOL_CHANGE_DETAIL_SCHEMA.
func (d *SymbolChangeDetail) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(d.Name, 1, []string{"name"}))
	if len(d.ChangeKinds) < 1 {
		Collect(&issues, &Issue{Path: []string{"changeKinds"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i, kind := range d.ChangeKinds {
		Collect(&issues, CheckEnum(string(kind), []string{"signature", "type", "visibility"}, []string{"changeKinds", itoa(i)}))
	}
	return issues
}

// SymbolsDetail is the name-level projection of a per-file symbol diff
// (P11-T03, E2): added/removed symbol names and changed symbols with their
// semantic change kinds. The full structured evidence (signatures, lines,
// before/after annotations) stays in the analyzer layer; the record carries
// the declared minimal enrichment next to `symbols_changed`
// (SYMBOLS_DETAIL_SCHEMA).
type SymbolsDetail struct {
	Added   []string             `json:"added"`
	Removed []string             `json:"removed"`
	Changed []SymbolChangeDetail `json:"changed"`
}

// Validate checks the detail against SYMBOLS_DETAIL_SCHEMA.
func (d *SymbolsDetail) Validate() []Issue {
	var issues []Issue
	for i, name := range d.Added {
		Collect(&issues, RequiredString(name, 1, []string{"added", itoa(i)}))
	}
	for i, name := range d.Removed {
		Collect(&issues, RequiredString(name, 1, []string{"removed", itoa(i)}))
	}
	for i := range d.Changed {
		for _, iss := range d.Changed[i].Validate() {
			iss.Path = append([]string{"changed", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ChangedFile is one changed file (CHANGED_FILE_SCHEMA).
type ChangedFile struct {
	Path           string     `json:"path"`
	Status         FileStatus `json:"status"`
	OldPath        *string    `json:"old_path,omitempty"`
	Generated      *bool      `json:"generated,omitempty"`
	TestOnly       *bool      `json:"test_only,omitempty"`
	FormattingOnly *bool      `json:"formatting_only,omitempty"`
	SymbolsChanged *bool      `json:"symbols_changed,omitempty"`
	APIDiff        *bool      `json:"api_diff,omitempty"`
	ConfigDiff     *bool      `json:"config_diff,omitempty"`
	// SymbolsDetail is the symbol-level evidence enrichment (P11-T03, E2 —
	// additive optional field, the ratified extension pattern): present only
	// when a symbol-capable analyzer derived a per-file symbol diff; absent =
	// no symbol evidence.
	SymbolsDetail *SymbolsDetail `json:"symbols_detail,omitempty"`
}

// Validate checks the file against CHANGED_FILE_SCHEMA.
func (f *ChangedFile) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(f.Path, 1, []string{"path"}))
	Collect(&issues, CheckEnum(string(f.Status), []string{"added", "modified", "deleted", "renamed"}, []string{"status"}))
	if f.OldPath != nil {
		Collect(&issues, RequiredString(*f.OldPath, 1, []string{"old_path"}))
	}
	if f.SymbolsDetail != nil {
		for _, iss := range f.SymbolsDetail.Validate() {
			iss.Path = append([]string{"symbols_detail"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ClassificationHint is one classification hint (CLASSIFICATION_HINT_SCHEMA).
type ClassificationHint struct {
	Kind ClassificationHintKind `json:"kind"`
	Note string                 `json:"note"`
}

// Validate checks the hint against CLASSIFICATION_HINT_SCHEMA.
func (h *ClassificationHint) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(h.Kind), []string{"test_only", "formatting_only", "generated", "other"}, []string{"kind"}))
	return issues
}

// DeepAnalysis is the deep-analysis record (DEEP_ANALYSIS_SCHEMA).
type DeepAnalysis struct {
	Status DeepAnalysisStatus `json:"status"`
	Reason *string            `json:"reason,omitempty"`
}

// Validate checks the record against DEEP_ANALYSIS_SCHEMA.
func (d *DeepAnalysis) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(d.Status), []string{"performed", "skipped"}, []string{"status"}))
	return issues
}

// AnalyzerCoverage is one analyzer's declared degradation
// (ANALYZER_COVERAGE_SCHEMA).
type AnalyzerCoverage struct {
	Analyzer AnalyzerName   `json:"analyzer"`
	Status   AnalyzerStatus `json:"status"`
	Note     *string        `json:"note,omitempty"`
}

// Validate checks the coverage entry against ANALYZER_COVERAGE_SCHEMA.
func (a *AnalyzerCoverage) Validate() []Issue {
	var issues []Issue
	Collect(&issues, CheckEnum(string(a.Analyzer), []string{"typescript", "generic"}, []string{"analyzer"}))
	Collect(&issues, CheckEnum(string(a.Status), []string{"ok", "unavailable", "stub"}, []string{"status"}))
	return issues
}

// ChangeAnalysis is the deterministic pre-AI analysis of one run
// (CHANGE_ANALYSIS_SCHEMA).
type ChangeAnalysis struct {
	Schema              string               `json:"schema"`
	RunID               string               `json:"run_id"`
	Repo                string               `json:"repo"`
	BaseSHA             string               `json:"base_sha"`
	HeadSHA             string               `json:"head_sha"`
	Files               []ChangedFile        `json:"files"`
	DependenciesChanged bool                 `json:"dependencies_changed"`
	ClassificationHints []ClassificationHint `json:"classification_hints"`
	DeepAnalysis        DeepAnalysis         `json:"deep_analysis"`
	AnalyzerCoverage    []AnalyzerCoverage   `json:"analyzer_coverage"`
}

// Validate checks the analysis against CHANGE_ANALYSIS_SCHEMA.
func (c *ChangeAnalysis) Validate() []Issue {
	var issues []Issue
	if c.Schema != ChangeAnalysisSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"" + ChangeAnalysisSchemaID + "\""})
	}
	Collect(&issues, RequiredString(c.RunID, 1, []string{"run_id"}))
	Collect(&issues, RequiredString(c.Repo, 1, []string{"repo"}))
	Collect(&issues, RequiredString(c.BaseSHA, 1, []string{"base_sha"}))
	Collect(&issues, RequiredString(c.HeadSHA, 1, []string{"head_sha"}))
	for i := range c.Files {
		for _, iss := range c.Files[i].Validate() {
			iss.Path = append([]string{"files", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for i := range c.ClassificationHints {
		for _, iss := range c.ClassificationHints[i].Validate() {
			iss.Path = append([]string{"classification_hints", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	for _, iss := range c.DeepAnalysis.Validate() {
		iss.Path = append([]string{"deep_analysis"}, iss.Path...)
		issues = append(issues, iss)
	}
	for i := range c.AnalyzerCoverage {
		for _, iss := range c.AnalyzerCoverage[i].Validate() {
			iss.Path = append([]string{"analyzer_coverage", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// typescriptStubNote is the canonical stub note — verbatim
// `TYPESCRIPT_STUB_NOTE` from packages/analyzer-typescript/src/index.ts (the
// schemas package cannot import it: dependency direction core/schemas ←
// analyzers; keep in sync, Phase 5 golden run-records catch drift).
const typescriptStubNote = "stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap"

// caString returns a pointer to s, for optional string fields in the example.
func caString(s string) *string { return &s }

// caBool returns a pointer to b, for optional boolean fields in the example.
func caBool(b bool) *bool { return &b }

// ChangeAnalysisExample is the deterministic example (CHANGE_ANALYSIS_EXAMPLE).
var ChangeAnalysisExample = ChangeAnalysis{
	Schema:  ChangeAnalysisSchemaID,
	RunID:   "acme/payments#8a73f2c..81af92e",
	Repo:    "acme/payments",
	BaseSHA: "8a73f2c",
	HeadSHA: "81af92e",
	Files: []ChangedFile{
		{
			Path:           "src/payments/refund.ts",
			Status:         FileStatusModified,
			Generated:      caBool(false),
			TestOnly:       caBool(false),
			FormattingOnly: caBool(false),
			SymbolsChanged: caBool(true),
			APIDiff:        caBool(false),
			ConfigDiff:     caBool(false),
			SymbolsDetail: &SymbolsDetail{
				Added:   []string{},
				Removed: []string{},
				Changed: []SymbolChangeDetail{{Name: "idempotencyKey", ChangeKinds: []SymbolChangeKind{SymbolChangeType}}},
			},
		},
		{
			Path:     "src/payments/refund.test.ts",
			Status:   FileStatusAdded,
			TestOnly: caBool(true),
		},
	},
	DependenciesChanged: false,
	ClassificationHints: []ClassificationHint{
		{Kind: ClassificationHintTestOnly, Note: "all changed paths match src/**/*.test.ts"},
	},
	DeepAnalysis: DeepAnalysis{
		Status: DeepAnalysisSkipped,
		Reason: caString("no type-aware analyzer coverage for this language"),
	},
	AnalyzerCoverage: []AnalyzerCoverage{
		{Analyzer: AnalyzerNameTypeScript, Status: AnalyzerStatusStub, Note: caString(typescriptStubNote)},
		{Analyzer: AnalyzerNameGeneric, Status: AnalyzerStatusOK},
	},
}

// ParseChangeAnalysis strictly parses and validates a change-analysis
// document, mirroring the zod strictObject contract (unknown keys rejected).
func ParseChangeAnalysis(data []byte) (*ChangeAnalysis, error) {
	var c ChangeAnalysis
	if err := StrictDecode(data, &c); err != nil {
		return nil, err
	}
	if issues := c.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}
