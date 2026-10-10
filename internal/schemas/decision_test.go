package schemas_test

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func withAbstain(abstain bool, abstainReason *string) schemas.Decision {
	d := schemas.DecisionExample
	d.Abstain = abstain
	if abstainReason != nil {
		reason := schemas.AbstainReason(*abstainReason)
		d.AbstainReason = &reason
	} else {
		d.AbstainReason = nil
	}
	return d
}

func hasIssuePath(issues []schemas.Issue, want string) bool {
	for _, iss := range issues {
		for _, p := range iss.Path {
			if p == want {
				return true
			}
		}
	}
	return false
}

// decisionJSONWith marshals the golden example and applies a mutation at the
// JSON level (for cases no Go struct value can express, e.g. a missing key).
func decisionJSONWith(t *testing.T, mutate func(m map[string]any)) []byte {
	t.Helper()
	raw, err := json.Marshal(schemas.DecisionExample)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	mutate(m)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

func TestDecisionSchema_ValidatesARCH54WireExample(t *testing.T) {
	require.Empty(t, schemas.DecisionExample.Validate())

	raw, err := json.Marshal(schemas.DecisionExample)
	require.NoError(t, err)
	parsed, err := schemas.ParseDecision(raw)
	require.NoError(t, err)
	require.Equal(t, schemas.DecisionExample, *parsed)
}

func TestDecisionSchema_ExampleValidity(t *testing.T) {
	require.Empty(t, schemas.DecisionExample.Validate())
	require.Empty(t, schemas.DecisionAbstainExample.Validate())
}

func TestDecisionSchema_AbstainExampleRuleConsistency(t *testing.T) {
	// Rule-consistency with the documented confidence aggregation: the
	// deciding `documentation_impact` answer's probability IS the overall
	// confidence. 0.66 sits below the illustrative v17 review threshold
	// (0.72) → Stage-1 abstains `insufficient_confidence` (and is clear of
	// the calibration no-man's zone, which opens above the review threshold).
	require.Equal(t, 0.66, schemas.DecisionAbstainExample.Answers["documentation_impact"].Probability)
	require.Equal(t, 0.66, schemas.DecisionAbstainExample.Confidence)
	require.True(t, schemas.DecisionAbstainExample.Abstain)
	require.Equal(t, schemas.AbstainReasonInsufficientConfidence, *schemas.DecisionAbstainExample.AbstainReason)
}

func TestDecisionSchema_RoundTripsByteStable(t *testing.T) {
	raw, err := json.Marshal(schemas.DecisionAbstainExample)
	require.NoError(t, err)
	parsed, err := schemas.ParseDecision(raw)
	require.NoError(t, err)
	require.Equal(t, schemas.DecisionAbstainExample, *parsed)

	reparsed, err := json.Marshal(parsed)
	require.NoError(t, err)
	require.Equal(t, string(raw), string(reparsed))
}

func TestDecisionSchema_AbstainReasonTaxonomy(t *testing.T) {
	require.Equal(t, []schemas.AbstainReason{
		schemas.AbstainReasonInsufficientConfidence,
		schemas.AbstainReasonConflictingEvidence,
		schemas.AbstainReasonPolicyGap,
		schemas.AbstainReasonCalibrationFloor,
		schemas.AbstainReasonVerifierUncertain,
	}, schemas.DecisionLevelAbstainReasons)
	require.Equal(t, []schemas.AbstainReason{
		schemas.AbstainReasonSchemaViolation,
		schemas.AbstainReasonModelUnavailable,
		schemas.AbstainReasonCapExceeded,
		schemas.AbstainReasonPolicyMissing,
		schemas.AbstainReasonOther,
	}, schemas.OperationalAbstainReasons)

	// Stable order: five decision-level reasons first, operational extras,
	// `other` last.
	union := append(append([]schemas.AbstainReason{}, schemas.DecisionLevelAbstainReasons...), schemas.OperationalAbstainReasons...)
	require.Equal(t, union, schemas.AbstainReasonValues)

	// Deduped: every value is unique.
	seen := map[schemas.AbstainReason]bool{}
	for _, reason := range schemas.AbstainReasonValues {
		require.False(t, seen[reason], "duplicate abstain reason %q", reason)
		seen[reason] = true
	}
	// PLAN/ARCH ratification: the Phase-0 draft's `low_confidence` is renamed.
	require.NotContains(t, schemas.AbstainReasonValues, schemas.AbstainReason("low_confidence"))
	// Every taxonomy value passes the enum; unknown reasons are rejected.
	for _, reason := range schemas.AbstainReasonValues {
		require.True(t, schemas.IsValidAbstainReason(string(reason)))
	}
	require.False(t, schemas.IsValidAbstainReason("low_confidence"))
	require.False(t, schemas.IsValidAbstainReason("dont_know"))
}

func TestDecisionSchema_AbstainReasonPairingInvariant(t *testing.T) {
	// abstain true without a reason → reject.
	missingReason := withAbstain(true, nil)
	issues := missingReason.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "abstain_reason"))

	// reason without abstain → reject (decision-level and operational alike).
	for _, stray := range []string{"insufficient_confidence", "model_unavailable"} {
		strayReason := withAbstain(false, &stray)
		issues := strayReason.Validate()
		require.NotEmpty(t, issues)
		require.True(t, hasIssuePath(issues, "abstain_reason"))
	}

	// abstain true with a decision-level reason → accept.
	calibrationFloor := "calibration_floor"
	acceptCalibrationFloor := withAbstain(true, &calibrationFloor)
	require.Empty(t, acceptCalibrationFloor.Validate())
	// abstain true with an operational reason → accept.
	capExceeded := "cap_exceeded"
	acceptCapExceeded := withAbstain(true, &capExceeded)
	require.Empty(t, acceptCapExceeded.Validate())
}

func TestDecisionSchema_UniversalQuestionSeam(t *testing.T) {
	require.Equal(t, []string{
		"public_behavior_changed",
		"api_contract_changed",
		"affected_doc_count",
		"documentation_impact",
	}, schemas.UniversalQuestions)

	for key := range schemas.DecisionExample.Answers {
		require.Contains(t, schemas.UniversalQuestions, key)
	}
	for key := range schemas.DecisionAbstainExample.Answers {
		require.Contains(t, schemas.UniversalQuestions, key)
	}
	// The abstain example exercises the full universal set (§8.3.1: always runs).
	sortedUniversal := append([]string{}, schemas.UniversalQuestions...)
	sort.Strings(sortedUniversal)
	abstainKeys := make([]string, 0, len(schemas.DecisionAbstainExample.Answers))
	for key := range schemas.DecisionAbstainExample.Answers {
		abstainKeys = append(abstainKeys, key)
	}
	sort.Strings(abstainKeys)
	require.Equal(t, sortedUniversal, abstainKeys)
}

func TestDecisionSchema_RejectsAnswerProbabilityAboveOne(t *testing.T) {
	d := schemas.DecisionExample
	d.Answers = map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 1.2},
	}
	issues := d.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "probability"))
}

func TestDecisionSchema_RejectsNegativeProbabilityAndOutOfRangeConfidence(t *testing.T) {
	negativeProbability := schemas.DecisionExample
	negativeProbability.Answers = map[string]schemas.Answer{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: -0.1},
	}
	require.NotEmpty(t, negativeProbability.Validate())

	badConfidence := schemas.DecisionExample
	badConfidence.Confidence = 1.5
	issues := badConfidence.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "confidence"))
}

func TestDecisionSchema_RejectsAnswersMissingValueOrProbability(t *testing.T) {
	missingValue := decisionJSONWith(t, func(m map[string]any) {
		m["answers"] = map[string]any{
			"public_behavior_changed": map[string]any{"probability": 0.87},
		}
	})
	_, err := schemas.ParseDecision(missingValue)
	require.Error(t, err)
	require.Contains(t, err.Error(), "every answer must carry both a value and a probability (ARCH §5.4)")

	missingProbability := decisionJSONWith(t, func(m map[string]any) {
		m["answers"] = map[string]any{
			"public_behavior_changed": map[string]any{"value": true},
		}
	})
	_, err = schemas.ParseDecision(missingProbability)
	require.Error(t, err)
	require.Contains(t, err.Error(), "every answer must carry both a value and a probability (ARCH §5.4)")
}

func TestDecisionSchema_RejectsUnknownTopLevelKeys(t *testing.T) {
	extraField := decisionJSONWith(t, func(m map[string]any) {
		m["extra_field"] = true
	})
	_, err := schemas.ParseDecision(extraField)
	require.Error(t, err)
	require.Contains(t, err.Error(), "extra_field")
}

func TestDecisionSchema_RejectsInvalidEnums(t *testing.T) {
	badAction := schemas.DecisionExample
	badAction.Action = schemas.Action("auto")
	issues := badAction.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "action"))

	badRisk := schemas.DecisionExample
	badRisk.Risk = schemas.RiskLevel("extreme")
	issues = badRisk.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "risk"))

	badChangeKind := schemas.DecisionExample
	badChangeKind.ChangeKind = schemas.ChangeKind("whenever")
	issues = badChangeKind.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "change_kind"))
}

func TestDecisionSchema_RejectsNonIntegerOrNegativeLatencyMS(t *testing.T) {
	negativeLatency := schemas.DecisionExample
	negativeLatency.Trace.LatencyMS = -1
	issues := negativeLatency.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "latency_ms"))

	nonInteger := decisionJSONWith(t, func(m map[string]any) {
		m["trace"] = map[string]any{
			"model":       "jev",
			"policy_hash": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"latency_ms":  740.5,
		}
	})
	_, err := schemas.ParseDecision(nonInteger)
	require.Error(t, err)
}

func TestDecisionSchema_PolicyVersionMustBePositive(t *testing.T) {
	zeroVersion := schemas.DecisionExample
	zeroVersion.PolicyVersion = 0
	issues := zeroVersion.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "policy_version"))
}

/* ------------- canonical evidence gate self-pinning (V-P3-10; OD-2) ------
 *
 * Ported from packages/schemas/test/evidence-gate.test.ts: every EVIDENCE_KIND
 * value's rule is pinned VERBATIM — one valid ref passes, one invalid-shape
 * ref fails with the exact canonical rejection message — and the assertion
 * gate collects every offender with its location. Any taxonomy addition or
 * rule drift fails here; downstream consumers (decision emission, patch
 * evidence) are guarded against silent drift.
 */

type evidencePins struct {
	valid   []string
	invalid []struct {
		ref     string
		message string
	}
}

var perKindPins = map[string]evidencePins{
	"symbol_diff": {
		valid: []string{"src/a.ts:42", "src/payments/refund.ts:refundOrder"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "idempotencyKey became optional",
				message: "symbol_diff ref must be 'path:line' or 'path:symbolName' (got 'idempotencyKey became optional')",
			},
			{
				ref:     "/abs/path.ts:42",
				message: "symbol_diff path component '/abs/path.ts' is not a repo-relative path",
			},
			{
				ref:     "src/a.ts:1st",
				message: "symbol_diff anchor '1st' is neither a 1-based line number nor a symbol name",
			},
		},
	},
	"api_diff": {
		valid: []string{"src/payments/refund.ts"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "src/a.ts:42",
				message: "api_diff ref must be a repo-relative path without an anchor (got 'src/a.ts:42')",
			},
			{
				ref:     "../escape.ts",
				message: "api_diff ref must be a repo-relative path without an anchor (got '../escape.ts')",
			},
		},
	},
	"config_diff": {
		valid: []string{"config/app.yml:server.port"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "config/app.yml",
				message: "config_diff ref must be 'file:key' (got 'config/app.yml')",
			},
			{
				ref:     "/abs.yml:server.port",
				message: "config_diff file component '/abs.yml' is not a repo-relative path",
			},
			{
				ref:     "config/app.yml:",
				message: "config_diff ref must be 'file:key' (got 'config/app.yml:')",
			},
		},
	},
	"dependency_diff": {
		valid: []string{"lodash", "@scope/pkg-name"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "Lodash Thing",
				message: "dependency_diff ref must be an npm package name (got 'Lodash Thing')",
			},
			{
				ref:     "/abs/lodash",
				message: "dependency_diff ref must be an npm package name (got '/abs/lodash')",
			},
		},
	},
	"doc_anchor": {
		valid: []string{"docs/payments.md:12"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "docs/payments.md",
				message: "doc_anchor ref must be 'path:line' (got 'docs/payments.md')",
			},
			{
				ref:     "docs/payments.md:sym",
				message: "doc_anchor anchor 'sym' is not a 1-based line number",
			},
			{
				ref:     "docs/payments.md:0",
				message: "doc_anchor anchor '0' is not a 1-based line number",
			},
		},
	},
	"other": {
		valid: []string{"arbitrary free text anchor"},
		invalid: []struct {
			ref     string
			message string
		}{
			{
				ref:     "",
				message: "other evidence ref must be non-empty",
			},
		},
	},
}

func TestEvidenceGate_TaxonomyPinnedVerbatim(t *testing.T) {
	require.Equal(t, []schemas.EvidenceKind{
		schemas.EvidenceKindSymbolDiff,
		schemas.EvidenceKindAPIDiff,
		schemas.EvidenceKindConfigDiff,
		schemas.EvidenceKindDependencyDiff,
		schemas.EvidenceKindDocAnchor,
		schemas.EvidenceKindOther,
	}, schemas.EvidenceKindValues)

	for _, kind := range schemas.EvidenceKindValues {
		require.True(t, schemas.IsValidEvidenceKind(string(kind)))
		// Every taxonomy member has a rule pin in this suite (enumeration
		// complete — a new kind must grow this suite with it).
		_, pinned := perKindPins[string(kind)]
		require.True(t, pinned, "missing rule pin for kind %q", kind)
	}
	// The pin table names no kind outside the taxonomy (no stale pins).
	pinnedKinds := make([]string, 0, len(perKindPins))
	for kind := range perKindPins {
		pinnedKinds = append(pinnedKinds, kind)
	}
	sort.Strings(pinnedKinds)
	sortedValues := make([]string, 0, len(schemas.EvidenceKindValues))
	for _, kind := range schemas.EvidenceKindValues {
		sortedValues = append(sortedValues, string(kind))
	}
	sort.Strings(sortedValues)
	require.Equal(t, sortedValues, pinnedKinds)
}

func TestEvidenceGate_PerKindPinnedRefsResolveOrFailVerbatim(t *testing.T) {
	for kind, pins := range perKindPins {
		for _, ref := range pins.valid {
			require.Empty(t, schemas.EvidenceRefIssue(kind, ref), "'%s'", ref)
			require.True(t, schemas.IsResolvableEvidence(kind, ref), "'%s'", ref)
		}
		for _, invalid := range pins.invalid {
			require.Equal(t, invalid.message, schemas.EvidenceRefIssue(kind, invalid.ref), "'%s'", invalid.ref)
			require.False(t, schemas.IsResolvableEvidence(kind, invalid.ref), "'%s'", invalid.ref)
		}
	}
}

func TestEvidenceGate_AssertResolvableEvidenceCollectsAllOffenders(t *testing.T) {
	items := []schemas.EvidenceRef{
		{Kind: "symbol_diff", Ref: "free text no anchor"}, // offender
		{Kind: "api_diff", Ref: "src/a.ts"},               // VALID mid-array item — never collected
		{Kind: "config_diff", Ref: "config/app.yml"},      // offender
		{Kind: "dependency_diff", Ref: "Lodash Thing"},    // offender
		{Kind: "doc_anchor", Ref: "docs/x.md"},            // offender
		{Kind: "other", Ref: ""},                          // offender
	}
	err := schemas.AssertResolvableEvidence(items, "canonical enumeration")
	require.Error(t, err)

	var gate *schemas.EvidenceNotResolvableError
	require.True(t, errors.As(err, &gate))
	require.Equal(t, "EVIDENCE_NOT_RESOLVABLE", gate.Code())

	// Valid items are skipped; every offender carries its input-order index.
	require.Equal(t, []int{0, 2, 3, 4, 5}, []int{
		gate.Offending[0].Index, gate.Offending[1].Index, gate.Offending[2].Index,
		gate.Offending[3].Index, gate.Offending[4].Index,
	})
	require.Equal(t, []schemas.UnresolvableEvidenceItem{
		{
			Index:  0,
			Kind:   "symbol_diff",
			Ref:    "free text no anchor",
			Reason: "symbol_diff ref must be 'path:line' or 'path:symbolName' (got 'free text no anchor')",
		},
		{
			Index:  2,
			Kind:   "config_diff",
			Ref:    "config/app.yml",
			Reason: "config_diff ref must be 'file:key' (got 'config/app.yml')",
		},
		{
			Index:  3,
			Kind:   "dependency_diff",
			Ref:    "Lodash Thing",
			Reason: "dependency_diff ref must be an npm package name (got 'Lodash Thing')",
		},
		{
			Index:  4,
			Kind:   "doc_anchor",
			Ref:    "docs/x.md",
			Reason: "doc_anchor ref must be 'path:line' (got 'docs/x.md')",
		},
		{
			Index:  5,
			Kind:   "other",
			Ref:    "",
			Reason: "other evidence ref must be non-empty",
		},
	}, gate.Offending)

	// Canonical failure message: `where` prefix + V-P3-10 marker + offender list.
	require.Equal(t,
		"canonical enumeration: evidence refs must be machine-resolvable anchors "+
			"(ARCH §5.4; V-P3-10) — "+
			"[0] symbol_diff 'free text no anchor': symbol_diff ref must be 'path:line' or "+
			"'path:symbolName' (got 'free text no anchor'); "+
			"[2] config_diff 'config/app.yml': config_diff ref must be 'file:key' "+
			"(got 'config/app.yml'); "+
			"[3] dependency_diff 'Lodash Thing': dependency_diff ref must be an npm package name "+
			"(got 'Lodash Thing'); "+
			"[4] doc_anchor 'docs/x.md': doc_anchor ref must be 'path:line' (got 'docs/x.md'); "+
			"[5] other '': other evidence ref must be non-empty",
		gate.Error())
}

func TestEvidenceGate_AllValidKindsPassEmptyListPasses(t *testing.T) {
	valid := make([]schemas.EvidenceRef, 0, len(schemas.EvidenceKindValues))
	for _, kind := range schemas.EvidenceKindValues {
		valid = append(valid, schemas.EvidenceRef{Kind: string(kind), Ref: perKindPins[string(kind)].valid[0]})
	}
	require.NoError(t, schemas.AssertResolvableEvidence(valid, ""))
	require.NoError(t, schemas.AssertResolvableEvidence(nil, "canonical enumeration"))
}

func TestEvidenceGate_UnknownKindsNeverResolvable(t *testing.T) {
	require.Equal(t,
		"unknown evidence kind 'diary' — not a machine-resolvable anchor taxonomy member",
		schemas.EvidenceRefIssue("diary", "x"))
	require.False(t, schemas.IsResolvableEvidence("diary", "src/a.ts:42"))
}

func TestEvidenceGate_NotesNeverShapeChecked(t *testing.T) {
	// Free prose in `note` cannot unresolve a valid ref — the REF is the anchor.
	for _, note := range []string{
		"any free prose explanation, even instruction-like text, is allowed here",
		"untrusted <script>alert(1)</script>",
	} {
		item := schemas.EvidenceRef{Kind: string(schemas.EvidenceKindSymbolDiff), Ref: "src/a.ts:42", Note: note}
		require.True(t, schemas.IsResolvableEvidence(item.Kind, item.Ref), "%q", note)
		other := schemas.EvidenceRef{Kind: string(schemas.EvidenceKindOther), Ref: "ok", Note: note}
		require.True(t, schemas.IsResolvableEvidence(other.Kind, other.Ref), "%q", note)
	}
}

func TestDecisionSchema_SchemaLiteralAndRequiredStrings(t *testing.T) {
	badSchema := schemas.DecisionExample
	badSchema.Schema = "docsync.decision/v2"
	issues := badSchema.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "schema"))

	emptyChange := schemas.DecisionExample
	emptyChange.Change = schemas.DecisionChange{Commit: "", DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"}
	issues = emptyChange.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "commit"))

	emptyCandidate := schemas.DecisionExample
	emptyCandidate.Candidates = []string{"docs/payments.md", ""}
	issues = emptyCandidate.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "candidates"))

	badEvidence := schemas.DecisionExample
	badEvidence.Evidence = []schemas.Evidence{{Kind: schemas.EvidenceKindSymbolDiff, Ref: "", Note: "x"}}
	issues = badEvidence.Validate()
	require.NotEmpty(t, issues)
	require.True(t, hasIssuePath(issues, "ref"))
}

func TestDecisionEnums_ClosedVocabulariesReportMembership(t *testing.T) {
	for _, kind := range schemas.ChangeKindValues {
		require.True(t, schemas.IsValidChangeKind(string(kind)))
	}
	for _, impact := range schemas.DocumentationImpactValues {
		require.True(t, schemas.IsValidDocumentationImpact(string(impact)))
	}
	for _, risk := range schemas.RiskLevelValues {
		require.True(t, schemas.IsValidRiskLevel(string(risk)))
	}
	for _, action := range schemas.ActionValues {
		require.True(t, schemas.IsValidAction(string(action)))
	}
	require.False(t, schemas.IsValidChangeKind("whenever"))
	require.False(t, schemas.IsValidDocumentationImpact("maybe"))
	require.False(t, schemas.IsValidRiskLevel("extreme"))
	require.False(t, schemas.IsValidAction("auto"))
}

func TestEvidenceNotResolvableError_MapsToDocutilSchemaCode(t *testing.T) {
	err := schemas.AssertResolvableEvidence([]schemas.EvidenceRef{{Kind: "symbol_diff", Ref: "free text"}}, "decision emission")
	require.Error(t, err)
	var gate *schemas.EvidenceNotResolvableError
	require.True(t, errors.As(err, &gate))
	require.Equal(t, "EVIDENCE_NOT_RESOLVABLE", gate.Code())
	// Coded-error class mapping for docutil.CodeOf consumers.
	require.Equal(t, docutil.CodeSchema, docutil.CodeOf(err))
	// Default where-prefix when the caller passes "" (TS opts default).
	require.NoError(t, schemas.AssertResolvableEvidence(nil, ""))
}
