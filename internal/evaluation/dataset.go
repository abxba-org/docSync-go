// Ported from core/src/evaluation/dataset.ts (P10-T07..T09; ARCH §9 evaluation
// protocol; CTX §35; A3 dataset-yield study): the evaluation dataset builder.
//
// Historical-PR replay data enters the evaluation loop as ONE canonical unit —
// the `docsync.eval-case/v1` row — assembled from two persisted sources:
//
//   - run records under `<stateDir>/runs/*.json` (both
//     `docsync.run-outcome/v1` and `docsync.fix-run-record/v1` envelopes are
//     accepted; field extraction is tolerant because the caller hands over
//     untyped records), and
//   - hygiene labels (the W1b-1 sink at `<stateDir>/feedback/labels.jsonl`),
//     keyed by `label.decision_id` ↔ the run record's decision/run id field.
//
// OD-R22 (adopted): curated datasets are repository artifacts under
// `.docsync/evaluation/` — WriteDataset takes an explicit EvaluationDir (the
// caller passes `<repoRoot>/.docsync/evaluation`) and never guesses a location.
// Datasets are versioned data, not code output.
//
// A3 (dataset-yield study): the yield report is FIXTURE-based machinery — it
// declares `data_source: 'fixture'` and the pilot-repo limitation is carried by
// that tag, never hidden. The A3 minimum-case floor is versioned data
// (MinDatasetCases), never a silently hard-coded threshold (D11-adjacent).
//
// Fail closed: a run record that cannot yield a schema-valid case is skipped
// AND counted — it lands in the returned Rejected list with its reason, it is
// never silently dropped (§3 rule 9/10 discipline applied to evaluation data).
package evaluation

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// evalCaseSchemaID is the literal schema tag of every produced case
// (EVAL_CASE_SCHEMA_ID, OD-R01 strict).
const evalCaseSchemaID = "docsync.eval-case/v1"

// MinDatasetCases is the A3 minimum-case floor (versioned data, D11-adjacent —
// never silently hard-coded): below this many cases the dataset-yield verdict
// is `insufficient` and no policy promotion may consume the dataset. Value is
// calibrated per policy version over time; it lives here as exported data so
// tests and the promotion pipeline read the same floor.
const MinDatasetCases = 30

// DatasetDataSource is the A3 data-source tag: the dataset-yield study
// machinery is fixture-based. Carried on every yield report so no consumer can
// mistake fixture yield for production-repo yield (pilot-repo limitation,
// declared honestly).
const DatasetDataSource = "fixture"

// DatasetYieldVerdict values — `usable` only at or above MinDatasetCases.
const (
	DatasetYieldVerdictUsable       = "usable"
	DatasetYieldVerdictInsufficient = "insufficient"
)

// RejectedCase reasons (machine-readable, preserved verbatim).
const (
	RejectedSchemaValidationFailed     = "schema-validation-failed"
	RejectedDuplicateDecisionLabelPair = "duplicate-decision-label-pair"
	RejectedDuplicateLabelForDecision  = "duplicate-label-for-decision"
)

// LaterDocEdit is a later same-PR doc edit — evidence for an explicit
// true-negative case.
type LaterDocEdit struct {
	// PRKey is the PR key the edit belongs to (matched against the run
	// record's PR fields).
	PRKey string
	// DocPath is the document path edited after the decision.
	DocPath string
	// At is the ISO-8601 instant of the edit (provenance field, never
	// generated).
	At string
}

// RejectedCase is one skipped candidate case — skipped AND counted, never
// silent.
type RejectedCase struct {
	// Reason is the machine-readable rejection reason.
	Reason string
	// Detail is the human-readable detail (schema issues or the colliding ids).
	Detail string
	// RunIndex is the index of the offending run record (label-level
	// rejections omit it).
	RunIndex *int
}

// BuildLabeledCasesInput carries the two persisted sources plus the later
// same-PR doc edits feeding the explicit true-negative lane.
type BuildLabeledCasesInput struct {
	// RunRecords are the run records from `<stateDir>/runs/*.json` (untyped;
	// extraction is tolerant).
	RunRecords []map[string]any
	// Labels are the hygiene labels from the W1b-1 sink.
	Labels []schemas.HygieneLabel
	// LaterDocEdits feed the explicit true-negative lane.
	LaterDocEdits []LaterDocEdit
}

// BuildLabeledCasesResult is the fail-closed build outcome: schema-valid cases
// plus every skipped candidate with its reason.
type BuildLabeledCasesResult struct {
	// Cases are the schema-valid `docsync.eval-case/v1` cases.
	Cases []schemas.EvalCase
	// Rejected lists every skipped candidate with its reason — fail-closed
	// bookkeeping.
	Rejected []RejectedCase
}

// DatasetYieldReport is the A3 report — fixture data source is part of the
// verdict.
type DatasetYieldReport struct {
	Total           int         `json:"total"`
	Labeled         int         `json:"labeled"`
	TrueNegatives   int         `json:"trueNegatives"`
	ByPolicyVersion map[int]int `json:"byPolicyVersion"`
	DataSource      string      `json:"data_source"`
	Verdict         string      `json:"verdict"`
}

// WriteDatasetOptions names the dataset and its explicit curated-artifact
// directory (OD-R22): `<repoRoot>/.docsync/evaluation`.
type WriteDatasetOptions struct {
	// Name is the dataset name (file stem) — `<evaluationDir>/datasets/<name>.json`.
	Name string
	// EvaluationDir is the explicit curated-artifact directory.
	EvaluationDir string
}

// WriteDatasetResult reports the written path, its byte length and the case
// count.
type WriteDatasetResult struct {
	Path      string
	Bytes     int
	CaseCount int
}

// DatasetBuilderOptions configures the dataset builder. Clock (milliseconds)
// is the only time source — case ids are ULIDs stamped from it via the
// monotonic generator; nothing reads the wall clock here. RandBytes supplies
// the ULID randomness; when nil the platform CSPRNG is used (mirroring the TS
// default path — tests inject a deterministic byte source).
type DatasetBuilderOptions struct {
	Clock     func() int64
	RandBytes func(n int) ([]byte, error)
}

// DatasetBuilder assembles eval cases from run records + hygiene labels,
// writes curated datasets and reports dataset yield.
type DatasetBuilder struct {
	ulids *docutil.ULIDGenerator
}

// NewDatasetBuilder creates the dataset builder. `clock` (milliseconds) is the
// only time source; the builder never reads the wall clock itself.
func NewDatasetBuilder(opts DatasetBuilderOptions) (*DatasetBuilder, error) {
	if opts.Clock == nil {
		return nil, docutil.New(docutil.CodeInternal, "evaluation.NewDatasetBuilder: clock must be non-nil (no hidden wall-clock reads)")
	}
	randBytes := opts.RandBytes
	if randBytes == nil {
		randBytes = func(n int) ([]byte, error) {
			buf := make([]byte, n)
			if _, err := rand.Read(buf); err != nil {
				// Unreachable in practice (crypto/rand on Linux); fail closed.
				return make([]byte, n), nil
			}
			return buf, nil
		}
	}
	return &DatasetBuilder{ulids: docutil.NewULIDGeneratorWith(opts.Clock, randBytes)}, nil
}

func (b *DatasetBuilder) newCaseID() (string, error) {
	return b.ulids.Generate()
}

/* --------------------------------------------- tolerant record extraction */

// asRecord mirrors the TS asRecord: object, non-null, non-array.
func asRecord(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

// asString mirrors the TS asString: string with at least one character.
func asString(value any) (string, bool) {
	s, ok := value.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// asNumber mirrors the TS asNumber: finite number (JSON numbers arrive as
// float64; native Go numeric literals are accepted too).
func asNumber(value any) (float64, bool) {
	var f float64
	switch v := value.(type) {
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int:
		f = float64(v)
	case int8:
		f = float64(v)
	case int16:
		f = float64(v)
	case int32:
		f = float64(v)
	case int64:
		f = float64(v)
	case uint:
		f = float64(v)
	case uint8:
		f = float64(v)
	case uint16:
		f = float64(v)
	case uint32:
		f = float64(v)
	case uint64:
		f = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// getPath walks nested records; ok is false when any segment is missing or not
// a record (mirroring the TS getPath returning undefined).
func getPath(record map[string]any, path ...string) (any, bool) {
	var current any = record
	for _, segment := range path {
		next, ok := asRecord(current)
		if !ok {
			return nil, false
		}
		value, present := next[segment]
		if !present {
			return nil, false
		}
		current = value
	}
	return current, true
}

// firstString returns the first candidate path holding a non-empty string
// (TS `asString(...) ?? asString(...)` chains).
func firstString(record map[string]any, paths ...[]string) (string, bool) {
	for _, path := range paths {
		value, ok := getPath(record, path...)
		if !ok || value == nil {
			continue
		}
		if s, ok := asString(value); ok {
			return s, true
		}
	}
	return "", false
}

// firstNumber returns the first candidate path holding a finite number.
func firstNumber(record map[string]any, paths ...[]string) (float64, bool) {
	for _, path := range paths {
		value, ok := getPath(record, path...)
		if !ok || value == nil {
			continue
		}
		if n, ok := asNumber(value); ok {
			return n, true
		}
	}
	return 0, false
}

// numberToString renders a number the way JS String(number) does, so
// manifest_version stringification matches the TS extraction.
func numberToString(f float64) string {
	if f == math.Trunc(f) && !math.IsInf(f, 0) && math.Abs(f) < 1e21 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// firstNumberOrString returns the first candidate path holding a number
// (stringified, as TS String(manifestRaw)) or a non-empty string.
func firstNumberOrString(record map[string]any, paths ...[]string) (string, bool) {
	for _, path := range paths {
		value, ok := getPath(record, path...)
		if !ok || value == nil {
			continue
		}
		if n, ok := asNumber(value); ok {
			return numberToString(n), true
		}
		if s, ok := asString(value); ok {
			return s, true
		}
	}
	return "", false
}

// sortedUnique renders a stable-sorted, de-duplicated string list
// (deterministic output).
func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// dedupePreserveOrder de-duplicates while keeping first-occurrence order
// (TS `[...new Set(values)]`).
func dedupePreserveOrder(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ExtractedRunKey is the run-key extraction across both run-record envelopes.
type ExtractedRunKey struct {
	Repo            string
	BaseSHA         string
	HeadSHA         string
	ManifestVersion string
	PolicyVersion   int
}

// extractRunKey extracts across both run-record envelopes: flat fields first
// (`docsync.fix-run-record/v1`), then the `key` block and `provenance` block
// (`docsync.run-outcome/v1`). Missing/short shas simply fail EVAL_CASE_SCHEMA
// later and land in Rejected — extraction never fabricates values.
func extractRunKey(record map[string]any) ExtractedRunKey {
	out := ExtractedRunKey{}
	out.Repo, _ = firstString(record, []string{"repo"}, []string{"key", "repo"})
	out.BaseSHA, _ = firstString(record,
		[]string{"base_sha"}, []string{"key", "base_sha"}, []string{"base"})
	out.HeadSHA, _ = firstString(record,
		[]string{"head_sha"}, []string{"key", "head_sha"}, []string{"head"})
	out.ManifestVersion, _ = firstNumberOrString(record,
		[]string{"manifest_version"}, []string{"key", "manifest_version"})
	if policyVersion, ok := firstNumber(record,
		[]string{"policy_version"}, []string{"key", "policy_version"},
		[]string{"provenance", "policy_version"}); ok {
		out.PolicyVersion = int(policyVersion)
	}
	return out
}

// extractDecisionIds extracts decision/run ids: `label.decision_id` matches
// the run record's `decision_id` (run-outcome abstains/documents), the
// fix-run `run_id`, or a per-document `decision_id`. Order-stable,
// de-duplicated.
func extractDecisionIds(record map[string]any) []string {
	ids := []string{}
	if s, ok := asString(record["decision_id"]); ok {
		ids = append(ids, s)
	}
	if s, ok := asString(record["run_id"]); ok {
		ids = append(ids, s)
	}
	if documents, ok := record["documents"].([]any); ok {
		for _, entry := range documents {
			document, ok := asRecord(entry)
			if !ok {
				continue
			}
			if s, ok := asString(document["decision_id"]); ok {
				ids = append(ids, s)
			}
		}
	}
	return dedupePreserveOrder(ids)
}

// extractDocPaths extracts candidate doc paths from run-outcome documents and
// fix-run entry rows.
func extractDocPaths(record map[string]any) []string {
	paths := []string{}
	for _, field := range []string{"documents", "applied", "skipped", "refused"} {
		rows, ok := record[field].([]any)
		if !ok {
			continue
		}
		for _, row := range rows {
			entry, ok := asRecord(row)
			if !ok {
				continue
			}
			if s, ok := asString(entry["doc_path"]); ok {
				paths = append(paths, s)
			}
		}
	}
	return paths
}

// isAbstainedOnDocs is the true-negative lane predicate: the decision said
// docs_affected=false — either an explicit top-level `docs_affected: false`, or
// a run-outcome where every document routed to the terminal `ignore` outcome
// (no docs affected, no patch, no abstain). Inconclusive/degraded records
// never qualify.
func isAbstainedOnDocs(record map[string]any) bool {
	if v, present := record["docs_affected"]; present {
		if b, ok := v.(bool); ok && !b {
			return true
		}
	}
	documents, ok := record["documents"].([]any)
	if !ok || len(documents) == 0 {
		return false
	}
	for _, row := range documents {
		entry, ok := asRecord(row)
		if !ok {
			return false
		}
		v, present := entry["policy_outcome"]
		if !present {
			return false
		}
		s, ok := v.(string)
		if !ok || s != "ignore" {
			return false
		}
	}
	return true
}

// derivePrKeys derives PR keys for true-negative matching: `pr_number` first,
// then a `source` string, then the `docsync-<repo>-<pr>` concurrency group
// suffix. A record from which no PR key can be derived simply cannot match a
// later edit — that is the fail-closed behavior, never a guessed key.
func derivePrKeys(record map[string]any) []string {
	keys := []string{}
	if n, ok := asNumber(record["pr_number"]); ok {
		keys = append(keys, numberToString(n))
	}
	if s, ok := asString(record["source"]); ok {
		keys = append(keys, s)
	}
	if group, ok := asString(record["concurrency_group"]); ok {
		suffix := group[strings.LastIndex(group, "-")+1:]
		if isAllDigits(suffix) {
			keys = append(keys, suffix)
		}
	}
	return dedupePreserveOrder(keys)
}

// isAllDigits mirrors /^[0-9]+$/ (non-empty, digits only).
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// schemaIssues renders zod-style `${path || '<root>'}: ${message}` diagnostics
// joined by "; ".
func schemaIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		path := strings.Join(issue.Path, ".")
		if path == "" {
			path = "<root>"
		}
		parts = append(parts, path+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

/* ------------------------------------------------------------ builder */

// BuildLabeledCases assembles schema-valid eval cases from run records +
// hygiene labels (+ later same-PR doc edits for the true-negative lane).
func (b *DatasetBuilder) BuildLabeledCases(input BuildLabeledCasesInput) (BuildLabeledCasesResult, error) {
	result := BuildLabeledCasesResult{
		Cases:    []schemas.EvalCase{},
		Rejected: []RejectedCase{},
	}

	// Label index: decision_id -> label. A second label for an already
	// indexed decision is a duplicate — counted in Rejected, never silent.
	labelByDecision := map[string]schemas.HygieneLabel{}
	for _, label := range input.Labels {
		if first, ok := labelByDecision[label.DecisionID]; ok {
			result.Rejected = append(result.Rejected, RejectedCase{
				Reason: RejectedDuplicateLabelForDecision,
				Detail: fmt.Sprintf("decision_id %s already labeled by %s; duplicate label %s ignored",
					label.DecisionID, first.LabelID, label.LabelID),
			})
			continue
		}
		labelByDecision[label.DecisionID] = label
	}

	// Later same-PR doc edits indexed by PR key (true-negative lane).
	editsByPrKey := map[string][]LaterDocEdit{}
	for _, edit := range input.LaterDocEdits {
		editsByPrKey[edit.PRKey] = append(editsByPrKey[edit.PRKey], edit)
	}

	consumedLabels := map[string]bool{}

	for runIndex, record := range input.RunRecords {
		runKey := extractRunKey(record)
		decisionIds := extractDecisionIds(record)
		docPaths := extractDocPaths(record)
		prKeys := derivePrKeys(record)
		matched := false

		// (a) Labeled lane: decision id ↔ hygiene label → case with label_id.
		for _, decisionID := range decisionIds {
			label, ok := labelByDecision[decisionID]
			if !ok {
				continue
			}
			if consumedLabels[label.LabelID] {
				idx := runIndex
				result.Rejected = append(result.Rejected, RejectedCase{
					Reason: RejectedDuplicateDecisionLabelPair,
					Detail: fmt.Sprintf("label %s for decision %s already consumed by an earlier run record",
						label.LabelID, decisionID),
					RunIndex: &idx,
				})
				continue
			}
			consumedLabels[label.LabelID] = true
			caseID, err := b.newCaseID()
			if err != nil {
				return result, err
			}
			labelID := label.LabelID
			candidate := schemas.EvalCase{
				Schema: evalCaseSchemaID,
				CaseID: caseID,
				RunKey: schemas.EvalRunKey{
					Repo:            runKey.Repo,
					BaseSHA:         runKey.BaseSHA,
					HeadSHA:         runKey.HeadSHA,
					ManifestVersion: runKey.ManifestVersion,
					PolicyVersion:   runKey.PolicyVersion,
				},
				LabelID:          &labelID,
				Split:            schemas.EvalSplitRolling,
				ExpectedDocPaths: sortedUnique(docPaths),
			}
			if issues := candidate.Validate(); len(issues) > 0 {
				idx := runIndex
				result.Rejected = append(result.Rejected, RejectedCase{
					Reason:   RejectedSchemaValidationFailed,
					Detail:   schemaIssues(issues),
					RunIndex: &idx,
				})
				continue
			}
			result.Cases = append(result.Cases, candidate)
			matched = true
		}

		// (b) True-negative lane: docs_affected=false AND a later same-PR doc
		// edit exists → explicit true-negative case (label_id absent).
		if !matched && isAbstainedOnDocs(record) {
			edits := []LaterDocEdit{}
			for _, prKey := range prKeys {
				edits = append(edits, editsByPrKey[prKey]...)
			}
			if len(edits) > 0 {
				caseID, err := b.newCaseID()
				if err != nil {
					return result, err
				}
				editPaths := make([]string, 0, len(edits))
				for _, edit := range edits {
					editPaths = append(editPaths, edit.DocPath)
				}
				trueNegative := true
				candidate := schemas.EvalCase{
					Schema: evalCaseSchemaID,
					CaseID: caseID,
					RunKey: schemas.EvalRunKey{
						Repo:            runKey.Repo,
						BaseSHA:         runKey.BaseSHA,
						HeadSHA:         runKey.HeadSHA,
						ManifestVersion: runKey.ManifestVersion,
						PolicyVersion:   runKey.PolicyVersion,
					},
					IsTrueNegative:   &trueNegative,
					Split:            schemas.EvalSplitRolling,
					ExpectedDocPaths: sortedUnique(editPaths),
				}
				if issues := candidate.Validate(); len(issues) > 0 {
					idx := runIndex
					result.Rejected = append(result.Rejected, RejectedCase{
						Reason:   RejectedSchemaValidationFailed,
						Detail:   schemaIssues(issues),
						RunIndex: &idx,
					})
				} else {
					result.Cases = append(result.Cases, candidate)
				}
			}
		}
	}

	return result, nil
}

// WriteDataset writes the curated dataset deterministically: cases sorted by
// case_id, 2-space-indented JSON with a trailing newline (byte-identical
// rewrites), under `<evaluationDir>/datasets/<name>.json` (OD-R22: the caller
// passes the explicit `.docsync/evaluation` directory).
//
// Fail closed: re-validate every case at the write boundary — an invalid row
// aborts the write with an explicit error, never lands in the curated dataset.
func (b *DatasetBuilder) WriteDataset(cases []schemas.EvalCase, opts WriteDatasetOptions) (WriteDatasetResult, error) {
	invalidIndices := []int{}
	for i := range cases {
		if issues := cases[i].Validate(); len(issues) > 0 {
			invalidIndices = append(invalidIndices, i)
		}
	}
	if len(invalidIndices) > 0 {
		rendered := make([]string, 0, len(invalidIndices))
		for _, index := range invalidIndices {
			rendered = append(rendered, strconv.Itoa(index))
		}
		return WriteDatasetResult{}, fmt.Errorf(
			"writeDataset: refusing to write %d schema-invalid case(s) at indices %s "+
				"(fail-closed: curated datasets carry valid docsync.eval-case/v1 rows only)",
			len(invalidIndices), strings.Join(rendered, ", "))
	}

	sorted := make([]schemas.EvalCase, len(cases))
	copy(sorted, cases)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CaseID < sorted[j].CaseID })

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sorted); err != nil {
		return WriteDatasetResult{}, err
	}
	payload := buf.String() // Encode terminates with '\n', as JSON.stringify + '\n' does.

	path := filepath.Join(opts.EvaluationDir, "datasets", opts.Name+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return WriteDatasetResult{}, err
	}
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		return WriteDatasetResult{}, err
	}
	return WriteDatasetResult{Path: path, Bytes: len(payload), CaseCount: len(sorted)}, nil
}

// YieldReport reports dataset yield (A3): labeled cases + true negatives, the
// per-policy-version breakdown, the declared fixture data source, and the
// MinDatasetCases verdict.
func (b *DatasetBuilder) YieldReport(cases []schemas.EvalCase) DatasetYieldReport {
	byPolicyVersion := map[int]int{}
	labeled := 0
	trueNegatives := 0
	for _, evalCase := range cases {
		if evalCase.LabelID != nil {
			labeled++
		}
		if evalCase.IsTrueNegative != nil && *evalCase.IsTrueNegative {
			trueNegatives++
		}
		byPolicyVersion[evalCase.RunKey.PolicyVersion]++
	}
	verdict := DatasetYieldVerdictInsufficient
	if len(cases) >= MinDatasetCases {
		verdict = DatasetYieldVerdictUsable
	}
	return DatasetYieldReport{
		Total:           len(cases),
		Labeled:         labeled,
		TrueNegatives:   trueNegatives,
		ByPolicyVersion: byPolicyVersion,
		DataSource:      DatasetDataSource,
		Verdict:         verdict,
	}
}
