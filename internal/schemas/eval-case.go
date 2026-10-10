// Ported from packages/schemas/src/v1/eval-case.ts (EVAL_CASE_SCHEMA_ID +
// EVAL_SPLIT_VALUES + EVAL_SPLIT + EVAL_CASE_SCHEMA + EvalCase +
// EVAL_CASE_EXAMPLE).
package schemas

import "github.com/DUDE616/docSync-go/internal/docutil"

// Evaluation case — `docsync.eval-case/v1` (P10-T01; ARCH §9 evaluation
// protocol). An evaluation case pins ONE decision to its frozen-or-rolling
// split membership, its ground truth (a hygiene label, or the explicit
// true-negative marker for decisions that correctly abstained), and the
// document paths the evaluation expects to be affected. The run key
// (repo + base/head sha + manifest/policy version) makes every case
// replayable against the exact code state it scored (ARCH §6.3 pin
// discipline, applied to evaluation).
//
// Shape notes: flat envelope with a literal `schema` tag (OD-R01 strict,
// OD-2 vocabulary reuse); exactly-one-of ground truth (superRefine, fail
// closed) — `label_id` XOR `is_true_negative`, never both, never neither;
// split membership is a closed two-value union (frozen | rolling) with
// thresholds living in policy (D11), never here; git shas reuse the
// provenance GIT_SHA_REGEX (OD-2: import, never duplicate) and case ids
// reuse the ULID vocabulary.

// EvalCaseSchemaID is the wire literal carried by the `schema` field
// (z.literal('docsync.eval-case/v1')).
const EvalCaseSchemaID = "docsync.eval-case/v1"

// EvalSplit is one split-membership value (EVAL_SPLIT): frozen (benchmark)
// vs rolling (recent window).
type EvalSplit string

// Eval split values in canonical order (EVAL_SPLIT_VALUES).
const (
	EvalSplitFrozen  EvalSplit = "frozen"
	EvalSplitRolling EvalSplit = "rolling"
)

// EvalSplitValues lists every split value in canonical order
// (EVAL_SPLIT_VALUES).
var EvalSplitValues = []EvalSplit{EvalSplitFrozen, EvalSplitRolling}

// EvalRunKey pins one case to the exact code + policy state it scored
// (the run_key strictObject). manifest_version is a string identifier here
// (the run key elsewhere pins it as a number — consumers comparing the two
// must stringify, the documented P9-T01 asymmetry).
type EvalRunKey struct {
	Repo            string `json:"repo"`
	BaseSHA         string `json:"base_sha"`
	HeadSHA         string `json:"head_sha"`
	ManifestVersion string `json:"manifest_version"`
	PolicyVersion   int    `json:"policy_version"`
}

// Validate checks the run key, reproducing the zod issue paths.
func (k *EvalRunKey) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(k.Repo, 1, []string{"repo"}))
	if !IsGitSHA(k.BaseSHA) {
		Collect(&issues, &Issue{Path: []string{"base_sha"}, Message: "Invalid"})
	}
	if !IsGitSHA(k.HeadSHA) {
		Collect(&issues, &Issue{Path: []string{"head_sha"}, Message: "Invalid"})
	}
	Collect(&issues, RequiredString(k.ManifestVersion, 1, []string{"manifest_version"}))
	if k.PolicyVersion < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// EvalCase is one replayable evaluation case — pinned to its exact code +
// policy state (EVAL_CASE_SCHEMA). label_id and is_true_negative are the
// exactly-one-of ground truth (superRefine enforces the XOR).
type EvalCase struct {
	Schema           string     `json:"schema"`
	CaseID           string     `json:"case_id"`
	RunKey           EvalRunKey `json:"run_key"`
	LabelID          *string    `json:"label_id,omitempty"`
	IsTrueNegative   *bool      `json:"is_true_negative,omitempty"`
	Split            EvalSplit  `json:"split"`
	ExpectedDocPaths []string   `json:"expected_doc_paths"`
}

// Validate checks the case against EVAL_CASE_SCHEMA (strict), reproducing
// the zod issue paths — field checks first, the exactly-one-of superRefine
// last.
func (c *EvalCase) Validate() []Issue {
	var issues []Issue
	if c.Schema != EvalCaseSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: `Invalid literal value, expected "docsync.eval-case/v1"`})
	}
	if !docutil.IsValidULID(c.CaseID) {
		Collect(&issues, &Issue{Path: []string{"case_id"}, Message: "Invalid"})
	}
	for _, iss := range c.RunKey.Validate() {
		iss.Path = append([]string{"run_key"}, iss.Path...)
		issues = append(issues, iss)
	}
	if c.LabelID != nil && !docutil.IsValidULID(*c.LabelID) {
		Collect(&issues, &Issue{Path: []string{"label_id"}, Message: "Invalid"})
	}
	Collect(&issues, CheckEnum(string(c.Split), stringsOf(EvalSplitValues), []string{"split"}))
	for i, p := range c.ExpectedDocPaths {
		Collect(&issues, RequiredString(p, 1, []string{"expected_doc_paths", itoa(i)}))
	}
	// Exactly-one-of ground truth (fail closed): label_id and
	// is_true_negative are mutually exclusive and jointly mandatory — a case
	// with neither (or both) cannot be scored.
	if (c.LabelID != nil) == (c.IsTrueNegative != nil) {
		path := []string{"label_id"}
		if c.LabelID != nil {
			path = []string{"is_true_negative"}
		}
		Collect(&issues, &Issue{
			Path:    path,
			Message: "exactly one of label_id or is_true_negative is required (never both, never neither)",
		})
	}
	return issues
}

// ParseEvalCase strictly decodes and validates a `docsync.eval-case/v1`
// document (unknown JSON keys are rejected, mirroring z.strictObject).
func ParseEvalCase(data []byte) (*EvalCase, error) {
	var c EvalCase
	if err := StrictDecode(data, &c); err != nil {
		return nil, err
	}
	if issues := c.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &c, nil
}

// EvalCaseExample mirrors EVAL_CASE_EXAMPLE (validated example: a labeled
// frozen-split case pinned to its code state; golden data for byte-for-byte
// serializer checks).
func EvalCaseExample() EvalCase {
	return EvalCase{
		Schema: EvalCaseSchemaID,
		CaseID: "01J9ZM6T0CZA6D8E0A2C4D6E8F",
		RunKey: EvalRunKey{
			Repo:            "acme/widgets",
			BaseSHA:         "3f9c2a7b1d4e5f60718293a4b5c6d7e8f9012a3b",
			HeadSHA:         "9b1deb4d3b7d4bec9a2c0f1e8d7c6b5a49382716",
			ManifestVersion: "1",
			PolicyVersion:   17,
		},
		LabelID:          strPtr("01J9ZM4R9BYZ5C7D9E1F3A5B7C"),
		Split:            EvalSplitFrozen,
		ExpectedDocPaths: []string{"docs/payments.md"},
	}
}
