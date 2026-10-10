package schemas_test

// Ported from packages/schemas/test/promotion-record.test.ts (black-box,
// testify), plus golden-data tests for the four companion Phase-12 schemas
// ported in shadow.go (docsync.shadow-agreement-matrix/v1 and
// docsync.shadow-run-record/v1), canary-record.go (docsync.canary-record/v1)
// and threshold-calibration.go (docsync.threshold-calibration/v1).
//
// The TS suite exercises promotion-record.ts; no TS test files exist for the
// Phase-12 schemas (verified: packages/schemas/test holds provenance, run-outcome
// and promotion-record only), so those four are pinned here as golden tests:
// the shipped examples parse, serialize byte-stable against the Go
// serializer, refuse unknown keys (z.strictObject), and reproduce the exact
// zod validation messages/paths.
//
// Helper names are promotion/shadow/canary/threshold-prefixed: sibling
// black-box test files in package schemas_test are written concurrently by
// other delegates, so shared generic helper names would redeclare.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* -------------------------------------------------------------- helpers */

// goldenRoundTrip parses the example and re-serializes the parsed value,
// asserting the Go serializer renders it byte-for-byte stable (TS *_EXAMPLE
// values are golden data). It returns the example's serialized bytes.
func goldenRoundTrip(t *testing.T, name string, example any, roundTrip func([]byte) ([]byte, error)) []byte {
	t.Helper()
	first, err := json.Marshal(example)
	require.NoError(t, err)
	second, err := roundTrip(first)
	require.NoError(t, err, "%s: example must parse", name)
	require.Equal(t, string(first), string(second), "%s: serializer must be byte-stable", name)
	return first
}

func wireOf(t *testing.T, example any, mutate func(map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(example)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	if mutate != nil {
		mutate(m)
	}
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// refusedWith asserts the parse failed with a message carrying needle.
func refusedWith(t *testing.T, data []byte, parse func([]byte) error, needle string) {
	t.Helper()
	err := parse(data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), needle)
}

// assertJSONKeys checks the record's top-level JSON keys (the zod-declared
// snake_case wire names — wire format is law).
func assertJSONKeys(t *testing.T, data []byte, want ...string) {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	got := make([]string, 0, len(raw))
	for k := range raw {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
}

// marshalOf re-marshals a decoded subtree for key assertions.
func marshalOf(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

/* ---------------------------------------- docsync.promotion-record/v1 */

// The shipped example parses and its fields survive the round trip (the TS
// suite's "the shipped example validates").
func TestP10T01_PromotionRecordExampleValidates(t *testing.T) {
	data := wireOf(t, schemas.PromotionRecordExample(), nil)
	parsed, err := schemas.ParsePromotionRecord(data)
	require.NoError(t, err)
	assert.Equal(t, schemas.PromotionRecordSchemaID, parsed.Schema)
	assert.Equal(t, string(schemas.PromotionStatusPromoted), string(parsed.Status))
	require.NotNil(t, parsed.PreviousVersion)
	assert.Equal(t, 1, *parsed.PreviousVersion)
	require.NotNil(t, parsed.ApprovedBy)
	assert.Equal(t, "repo-owner:@acme/maintainers", *parsed.ApprovedBy)
	assert.Equal(t, 140, parsed.Evaluation.SampleSize)
	assert.Equal(t, 0, parsed.Evaluation.UnsafeAutoUpdates)
	// z.record(z.unknown()) values keep their JSON number shape.
	assert.Equal(t, json.Number("2"), parsed.Evaluation.Report["policy_version"])
	assert.Equal(t, json.Number("1"), parsed.Evaluation.Report["baseline_version"])
	assert.Equal(t, json.Number("140"), parsed.Evaluation.Report["sample_size"])
}

func TestP10T01_PromotionRecordSerializationGolden(t *testing.T) {
	data := goldenRoundTrip(t, "promotion-record", schemas.PromotionRecordExample(), func(b []byte) ([]byte, error) {
		parsed, err := schemas.ParsePromotionRecord(b)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parsed)
	})
	assertJSONKeys(t, data, "approved_by", "at", "evaluation", "policy_version",
		"previous_version", "promotion_id", "reason", "schema", "status")
	var tree map[string]any
	require.NoError(t, json.Unmarshal(data, &tree))
	assertJSONKeys(t, marshalOf(t, tree["evaluation"]), "report", "sample_size", "unsafe_auto_updates")
	assertJSONKeys(t, marshalOf(t, tree["evaluation"].(map[string]any)["report"]),
		"baseline_version", "policy_version", "sample_size")
}

func TestP10T01_PromotionRecordApprovedPromotedRequireApprovedBy_ProposedDoesNot(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParsePromotionRecord(b); return err }
	withoutApprover := func(m map[string]any) {
		delete(m, "approved_by")
	}
	// promoted without an approver is refused (INV-7).
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		withoutApprover(m)
		m["status"] = "promoted"
	}), parse, "status approved/promoted requires approved_by — models propose, humans promote (INV-7)")
	// approved without an approver is refused (INV-7).
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		withoutApprover(m)
		m["status"] = "approved"
	}), parse, "status approved/promoted requires approved_by — models propose, humans promote (INV-7)")
	// proposed without an approver parses.
	parsed, err := schemas.ParsePromotionRecord(wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		withoutApprover(m)
		m["status"] = "proposed"
	}))
	require.NoError(t, err)
	assert.Nil(t, parsed.ApprovedBy)
	assert.Equal(t, string(schemas.PromotionStatusProposed), string(parsed.Status))
	// rolled_back without an approver parses too (only approved/promoted gate).
	_, err = schemas.ParsePromotionRecord(wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		withoutApprover(m)
		m["status"] = "rolled_back"
	}))
	require.NoError(t, err)
}

func TestP10T01_PromotionRecordUnsafeAutoUpdatesNonNegativeGateMetric_SampleSizePositive(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParsePromotionRecord(b); return err }
	// unsafe_auto_updates is a non-negative gate metric (not promotable away).
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		evaluation := m["evaluation"].(map[string]any)
		evaluation["unsafe_auto_updates"] = float64(-1)
	}), parse, "unsafe_auto_updates: Number must be greater than or equal to 0")
	// sample_size must be positive.
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		evaluation := m["evaluation"].(map[string]any)
		evaluation["sample_size"] = float64(0)
	}), parse, "sample_size: Number must be greater than 0")
}

func TestP10T01_PromotionRecordStatusVocabularyIsClosed(t *testing.T) {
	assert.Len(t, schemas.PromotionStatusValues, 4)
	assert.Equal(t, []schemas.PromotionStatus{
		schemas.PromotionStatusProposed, schemas.PromotionStatusApproved,
		schemas.PromotionStatusPromoted, schemas.PromotionStatusRolledBack,
	}, schemas.PromotionStatusValues)

	parse := func(b []byte) error { _, err := schemas.ParsePromotionRecord(b); return err }
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["status"] = "auto"
	}), parse, "Invalid enum value. Expected 'proposed' | 'approved' | 'promoted' | 'rolled_back', received 'auto'")
}

func TestP10T01_PromotionRecordRejectsUnknownKeysAndShapeViolations(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParsePromotionRecord(b); return err }
	// z.strictObject ⇒ unknown keys are rejected.
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["promoted_at_extra"] = "2026-10-08T12:00:00.000Z"
	}), parse, "unknown field")
	// The literal schema tag is enforced.
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["schema"] = "docsync.promotion-record/v2"
	}), parse, `Invalid literal value, expected "docsync.promotion-record/v1"`)
	// promotion_id must be a ULID (feedback-event.ts ULID_REGEX).
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["promotion_id"] = "not-a-ulid"
	}), parse, "promotion_id: Invalid")
	// Empty reason is refused.
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["reason"] = ""
	}), parse, "reason: String must contain at least 1 character(s)")
	// `at` is a UTC-only datetime (offset: false).
	refusedWith(t, wireOf(t, schemas.PromotionRecordExample(), func(m map[string]any) {
		m["at"] = "2026-10-08T14:00:00+02:00"
	}), parse, "at: Invalid datetime")
}

/* ------------------------- docsync.shadow-agreement-matrix/v1 (P12-T04) */

func TestP12T04_ShadowAgreementMatrixExampleValidatesAndSerializesGolden(t *testing.T) {
	data := goldenRoundTrip(t, "shadow-agreement-matrix", schemas.ShadowAgreementMatrixExample(), func(b []byte) ([]byte, error) {
		parsed, err := schemas.ParseShadowAgreementMatrix(b)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parsed)
	})
	assertJSONKeys(t, data, "agreement_count", "candidate_policy_version", "cells",
		"computed_at", "disagreement_count", "disagreements", "input_hashes",
		"production_policy_version", "schema", "split_id")
	parsed, err := schemas.ParseShadowAgreementMatrix(data)
	require.NoError(t, err)
	assert.Equal(t, 3, parsed.AgreementCount)
	assert.Equal(t, 1, parsed.DisagreementCount)
	assert.Equal(t, schemas.ShadowResolutionPending, parsed.Disagreements[0].Resolution)
	assert.Equal(t, 4, len(parsed.InputHashes))
}

func TestP12T04_ShadowAgreementMatrixCountsMustClassifyEveryCase(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowAgreementMatrix(b); return err }
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		m["agreement_count"] = float64(4)
	}), parse, "agreement_count: cells must classify exactly agreement_count + disagreement_count cases")
}

func TestP12T04_ShadowAgreementMatrixCasesAppearInExactlyOneCell(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowAgreementMatrix(b); return err }
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		cells := m["cells"].([]any)
		cell := cells[1].(map[string]any)
		cell["case_ids"] = []any{"case-01"}
	}), parse, "cells: case case-01 appears in more than one cell")
}

func TestP12T04_ShadowAgreementMatrixDisagreementRowsMirrorMixedCells(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowAgreementMatrix(b); return err }
	// Every disagreeing case needs a matching row.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		m["disagreements"] = []any{}
	}), parse, "disagreements: disagreeing case case-04 has no matching disagreement row")
	// Duplicate rows per case are refused.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		disagreements := m["disagreements"].([]any)
		m["disagreements"] = []any{disagreements[0], disagreements[0]}
	}), parse, "disagreements: duplicate case_id in disagreements")
	// A row for an agreeing case is refused.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		disagreements := m["disagreements"].([]any)
		row := disagreements[0].(map[string]any)
		row["case_id"] = "case-01"
	}), parse, "disagreements: case case-01 is in an agreeing cell but carries a disagreement row")
	// A row not backed by a mixed cell is refused.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		disagreements := m["disagreements"].([]any)
		row := disagreements[0].(map[string]any)
		row["case_id"] = "case-03"
	}), parse, "disagreements: disagreement case case-03 is not backed by a mixed cell")
}

func TestP12T04_ShadowAgreementMatrixInputHashesRecordIdenticalInputs(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowAgreementMatrix(b); return err }
	// equal must be the literal true (identical-input guarantee, V-P12-2).
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		inputHashes := m["input_hashes"].(map[string]any)
		caseOne := inputHashes["case-01"].(map[string]any)
		caseOne["equal"] = false
	}), parse, `input_hashes.case-01.equal: Invalid literal value, expected true`)
	// Input hashes must be sha256 hex.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		inputHashes := m["input_hashes"].(map[string]any)
		caseOne := inputHashes["case-01"].(map[string]any)
		caseOne["production"] = "8a73f2c"
	}), parse, "input_hashes.case-01.production: Invalid")
}

func TestP12T04_ShadowAgreementMatrixClosedVocabulariesAndUnknownKeys(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowAgreementMatrix(b); return err }
	// The decision vocabulary is imported from docsync.decision/v1 (OD-2).
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		cells := m["cells"].([]any)
		cell := cells[0].(map[string]any)
		cell["candidate_decision"] = "auto"
	}), parse, "Invalid enum value. Expected 'ignore' | 'review' | 'investigate' | 'patch', received 'auto'")
	// Disagreement resolutions are a closed vocabulary; pending until reviewed.
	assert.Equal(t, []schemas.ShadowDisagreementResolution{
		schemas.ShadowResolutionPending, schemas.ShadowResolutionCandidateCorrect,
		schemas.ShadowResolutionProductionCorrect, schemas.ShadowResolutionBothWrong,
	}, schemas.ShadowDisagreementResolutionValues)
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		disagreements := m["disagreements"].([]any)
		row := disagreements[0].(map[string]any)
		row["resolution"] = "candidate_won"
	}), parse, "Invalid enum value. Expected 'pending' | 'candidate_correct' | 'production_correct' | 'both_wrong', received 'candidate_won'")
	// z.strictObject ⇒ unknown keys are rejected.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		m["extra"] = true
	}), parse, "unknown field")
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		cells := m["cells"].([]any)
		cells[0].(map[string]any)["extra"] = true
	}), parse, "unknown field")
	// Empty case ids are refused.
	refusedWith(t, wireOf(t, schemas.ShadowAgreementMatrixExample(), func(m map[string]any) {
		cells := m["cells"].([]any)
		cell := cells[0].(map[string]any)
		cell["case_ids"] = []any{""}
	}), parse, "case_ids.0: String must contain at least 1 character(s)")
}

/* ------------------------------ docsync.shadow-run-record/v1 (P12-T02) */

func TestP12T02_ShadowRunRecordExampleValidatesAndSerializesGolden(t *testing.T) {
	data := goldenRoundTrip(t, "shadow-run-record", schemas.ShadowRunRecordExample(), func(b []byte) ([]byte, error) {
		parsed, err := schemas.ParseShadowRunRecord(b)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parsed)
	})
	assertJSONKeys(t, data, "created_at", "decision", "eligible", "expires_at",
		"input_hashes", "policy", "pr_id", "record_id", "repo", "run_id",
		"sampled", "sampling", "schema", "shadow_only")
	parsed, err := schemas.ParseShadowRunRecord(data)
	require.NoError(t, err)
	assert.True(t, parsed.ShadowOnly)
	assert.True(t, parsed.Sampled)
	assert.Equal(t, 0.1, parsed.Sampling.Fraction)
	require.NotNil(t, parsed.Decision)
	assert.Equal(t, string(schemas.ActionReview), string(parsed.Decision.Action))
}

func TestP12T02_ShadowRunRecordSampledRequiresDecisionAndHashes(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowRunRecord(b); return err }
	// Sampled without a decision is refused (fail closed).
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		delete(m, "decision")
	}), parse, "decision: a sampled shadow record must carry the recorded shadow decision")
	// Sampled without input hashes is refused (V-P12-2 lineage).
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		delete(m, "input_hashes")
	}), parse, "input_hashes: a sampled shadow record must record its input hashes (V-P12-2 lineage)")
	// Sampled with a skip reason is refused.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		m["skip_reason"] = "repo not eligible for shadow"
	}), parse, "skip_reason: a sampled shadow record must not carry a skip reason")
}

func TestP12T02_ShadowRunRecordUnsampledRequiresSkipReasonAndCarriesNoDecision(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowRunRecord(b); return err }
	unsample := func(m map[string]any) {
		m["sampled"] = false
		m["eligible"] = false
	}
	// An unsampled record with a decision is refused.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		unsample(m)
	}), parse, "decision: an unsampled shadow record must not carry a decision")
	// An unsampled record without input hashes parses, but must name its reason.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		unsample(m)
		delete(m, "input_hashes")
	}), parse, "skip_reason: an unsampled shadow record must name its skip reason")
	// A complete unsampled record parses.
	parsed, err := schemas.ParseShadowRunRecord(wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		unsample(m)
		delete(m, "input_hashes")
		delete(m, "decision")
		m["skip_reason"] = "repo not opted into learning.mode=canary|auto"
	}))
	require.NoError(t, err)
	assert.False(t, parsed.Sampled)
	require.NotNil(t, parsed.SkipReason)
	assert.Equal(t, "repo not opted into learning.mode=canary|auto", *parsed.SkipReason)
	assert.Nil(t, parsed.Decision)
}

func TestP12T02_ShadowRunRecordExpiresAtMustBeStrictlyAfterCreatedAt(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowRunRecord(b); return err }
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		m["expires_at"] = "2026-10-09T00:00:00.000Z"
	}), parse, "expires_at: expires_at must be strictly after created_at (time-boxed canary)")
}

func TestP12T02_ShadowRunRecordSamplingAndShadowOnlyMarkerAreEnforced(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseShadowRunRecord(b); return err }
	// The contract marker is a literal true — this record never surfaces to
	// developers.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		m["shadow_only"] = false
	}), parse, "shadow_only: Invalid literal value, expected true")
	// draw_hex is 8 lowercase hex chars; threshold_hex is 8-9.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["draw_hex"] = "0000000"
	}), parse, "draw_hex: Invalid")
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["threshold_hex"] = "1999999"
	}), parse, "threshold_hex: Invalid")
	// 10 hex chars is too long for the threshold shape.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["threshold_hex"] = "1999999999"
	}), parse, "threshold_hex: Invalid")
	// The 9-hex form is the documented fraction=1 edge — it parses.
	nineHex, err := schemas.ParseShadowRunRecord(wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["threshold_hex"] = "fffffffff"
	}))
	require.NoError(t, err)
	assert.Equal(t, "fffffffff", nineHex.Sampling.ThresholdHex)
	// fraction is a versioned policy-version fraction, bounded to [0, 1].
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["fraction"] = 1.5
	}), parse, "fraction: Number must be less than or equal to 1")
	// The sampler id is a closed one-value vocabulary.
	assert.Equal(t, []schemas.ShadowSampler{schemas.ShadowSamplerSHA256HexThreshold}, schemas.ShadowSamplerValues)
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		sampling := m["sampling"].(map[string]any)
		sampling["sampler"] = "random/v1"
	}), parse, "Invalid enum value. Expected 'sha256-hex-threshold/v1', received 'random/v1'")
	// z.strictObject ⇒ unknown keys are rejected.
	refusedWith(t, wireOf(t, schemas.ShadowRunRecordExample(), func(m map[string]any) {
		m["extra"] = true
	}), parse, "unknown field")
}

/* --------------------------------- docsync.canary-record/v1 (P12-T05) */

func TestP12T05_CanaryRecordExampleValidatesAndSerializesGolden(t *testing.T) {
	data := goldenRoundTrip(t, "canary-record", schemas.CanaryRecordExample(), func(b []byte) ([]byte, error) {
		parsed, err := schemas.ParseCanaryRecord(b)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parsed)
	})
	assertJSONKeys(t, data, "at", "candidate_policy_version", "canary_id", "created_at",
		"expires_at", "production_policy_version", "reason", "repo", "run_id",
		"schema", "status")
}

func TestP12T05_CanaryRecordCandidateAndProductionVersionsMustDiffer(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseCanaryRecord(b); return err }
	refusedWith(t, wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["production_policy_version"] = float64(18)
	}), parse, "candidate_policy_version: candidate and production policy versions must differ — a canary compares two policies")
}

func TestP12T05_CanaryRecordExpiresAtMustBeStrictlyAfterCreatedAt(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseCanaryRecord(b); return err }
	refusedWith(t, wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["expires_at"] = "2026-10-08T00:00:00.000Z"
	}), parse, "expires_at: expires_at must be strictly after created_at (time-boxed canary, ARCH §8.6)")
}

func TestP12T05_CanaryRecordStatusVocabularyIsClosed(t *testing.T) {
	assert.Equal(t, []schemas.CanaryStatus{
		schemas.CanaryStatusActive, schemas.CanaryStatusExpired,
		schemas.CanaryStatusPromoted, schemas.CanaryStatusRejected,
	}, schemas.CanaryStatusValues)
	parse := func(b []byte) error { _, err := schemas.ParseCanaryRecord(b); return err }
	// `expired` is a typed terminal outcome, so it parses.
	parsed, err := schemas.ParseCanaryRecord(wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["status"] = "expired"
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CanaryStatusExpired, parsed.Status)
	refusedWith(t, wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["status"] = "pending"
	}), parse, "Invalid enum value. Expected 'active' | 'expired' | 'promoted' | 'rejected', received 'pending'")
	// canary_id is a ULID.
	refusedWith(t, wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["canary_id"] = "01JCANARYEXAMP1ES00000000"
	}), parse, "canary_id: Invalid")
	// z.strictObject ⇒ unknown keys are rejected.
	refusedWith(t, wireOf(t, schemas.CanaryRecordExample(), func(m map[string]any) {
		m["extra"] = true
	}), parse, "unknown field")
}

/* ------------------------ docsync.threshold-calibration/v1 (P12-T08) */

func TestP12T08_ThresholdCalibrationExampleValidatesAndSerializesGolden(t *testing.T) {
	data := goldenRoundTrip(t, "threshold-calibration", schemas.ThresholdCalibrationExample(), func(b []byte) ([]byte, error) {
		parsed, err := schemas.ParseThresholdCalibration(b)
		if err != nil {
			return nil, err
		}
		return json.Marshal(parsed)
	})
	assertJSONKeys(t, data, "created_at", "created_by", "evaluation",
		"matrix_shape", "matrix_shape_hash", "policy_version",
		"previous_policy_version", "reason", "rolling_reeval_report", "schema",
		"thresholds", "time_split_report")
	var tree map[string]any
	require.NoError(t, json.Unmarshal(data, &tree))
	assertJSONKeys(t, marshalOf(t, tree["thresholds"]),
		"calibration_floor", "documentation_impact_auto_floor", "high_confidence", "review")
	assertJSONKeys(t, marshalOf(t, tree["rolling_reeval_report"]),
		"detail", "drift_detected", "drift_tolerance", "step", "window_size", "windows")
	// created_at accepts an offset (z.string().datetime() without offset:false).
	offset, err := schemas.ParseThresholdCalibration(wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["created_at"] = "2026-10-09T02:00:00+02:00"
	}))
	require.NoError(t, err)
	assert.Equal(t, "2026-10-09T02:00:00+02:00", offset.CreatedAt)
}

func TestP12T08_ThresholdCalibrationRoutingOrderingInvariants(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseThresholdCalibration(b); return err }
	// review must not exceed high_confidence.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		thresholds := m["thresholds"].(map[string]any)
		thresholds["review"] = 0.95
	}), parse, "thresholds.review: review must not exceed high_confidence (Routing ordering invariant; ARCH §5.3)")
	// calibration_floor must be at or above review (no-man's zone upper bound).
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		thresholds := m["thresholds"].(map[string]any)
		thresholds["calibration_floor"] = 0.5
	}), parse, "thresholds.calibration_floor: calibration_floor bounds the no-man's zone from above and must be ≥ review (ARCH §5.2)")
}

func TestP12T08_ThresholdCalibrationPolicyVersionMustStrictlyIncrease(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseThresholdCalibration(b); return err }
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["policy_version"] = float64(17)
	}), parse, "policy_version: policy_version must strictly increase over previous_policy_version — adaptive calibration never mutates an existing version in place (V-P12-10; D11)")
}

func TestP12T08_ThresholdCalibrationEvidenceAndReportsAreMinimumSized(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseThresholdCalibration(b); return err }
	// evidence_refs carries at least one ref (provenance).
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		evaluation := m["evaluation"].(map[string]any)
		evaluation["evidence_refs"] = []any{}
	}), parse, "evidence_refs: Array must contain at least 1 element(s)")
	// The split report needs at least two splits (R4 anti-overfit evidence).
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		timeSplitReport := m["time_split_report"].(map[string]any)
		timeSplitReport["splits"] = []any{}
	}), parse, "splits: Array must contain at least 2 element(s)")
	// The rolling report needs at least two windows.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		rollingReevalReport := m["rolling_reeval_report"].(map[string]any)
		rollingReevalReport["windows"] = []any{}
	}), parse, "windows: Array must contain at least 2 element(s)")
}

func TestP12T08_ThresholdCalibrationShapeAndDigestAreEnforced(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseThresholdCalibration(b); return err }
	assert.Equal(t, "fixed-3x3-v1", schemas.MatrixShapeV1)
	// matrix_shape is a literal so a shape change can never ride a data change.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["matrix_shape"] = "fixed-3x3-v2"
	}), parse, `matrix_shape: Invalid literal value, expected "fixed-3x3-v1"`)
	// matrix_shape_hash is a sha256:<hex> canonical-JSON digest.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["matrix_shape_hash"] = "82b30f0886a7a8558d2bf448fcdd056c9d3e04e9a09c4aae64f443db5484a418"
	}), parse, "matrix_shape_hash: Invalid")
	// Every threshold is bounded to the unit interval (out-of-range is
	// refused, never clamped).
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		thresholds := m["thresholds"].(map[string]any)
		thresholds["high_confidence"] = 1.5
	}), parse, "high_confidence: Number must be less than or equal to 1")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		timeSplitReport := m["time_split_report"].(map[string]any)
		timeSplitReport["overfit_tolerance"] = 0.5
		splits := timeSplitReport["splits"].([]any)
		splits[0].(map[string]any)["accuracy"] = -0.1
	}), parse, "splits.0.accuracy: Number must be greater than or equal to 0")
	// The example's own tolerance still validates.
	example := schemas.ThresholdCalibrationExample()
	require.Empty(t, example.Validate())
	_, err := schemas.ParseThresholdCalibration(wireOf(t, example, nil))
	require.NoError(t, err)
}

func TestP12T08_ThresholdCalibrationRejectsUnknownKeysAndBadShapes(t *testing.T) {
	parse := func(b []byte) error { _, err := schemas.ParseThresholdCalibration(b); return err }
	// z.strictObject ⇒ unknown keys are rejected at every level.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["extra"] = true
	}), parse, "unknown field")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		thresholds := m["thresholds"].(map[string]any)
		thresholds["extra"] = true
	}), parse, "unknown field")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		timeSplitReport := m["time_split_report"].(map[string]any)
		splits := timeSplitReport["splits"].([]any)
		splits[0].(map[string]any)["extra"] = true
	}), parse, "unknown field")
	// created_at / reason / created_by are required and shaped.
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["created_at"] = "not-a-date"
	}), parse, "created_at: Invalid datetime")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["created_by"] = ""
	}), parse, "created_by: String must contain at least 1 character(s)")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		m["previous_policy_version"] = float64(0)
	}), parse, "previous_policy_version: Number must be greater than 0")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		evaluation := m["evaluation"].(map[string]any)
		delete(evaluation, "evidence_refs")
	}), parse, "evidence_refs: Array must contain at least 1 element(s)")
	refusedWith(t, wireOf(t, schemas.ThresholdCalibrationExample(), func(m map[string]any) {
		rollingReevalReport := m["rolling_reeval_report"].(map[string]any)
		delete(rollingReevalReport, "detail")
	}), parse, "detail: String must contain at least 1 character(s)")
}

/* ------------------------- membership predicates and nested shapes */

func TestPhase12SchemasMembershipAndHashPredicates(t *testing.T) {
	// Closed vocabularies (the IsValid* membership predicates).
	assert.True(t, schemas.IsValidPromotionStatus("proposed"))
	assert.True(t, schemas.IsValidPromotionStatus("rolled_back"))
	assert.False(t, schemas.IsValidPromotionStatus("auto"))
	assert.True(t, schemas.IsValidCanaryStatus("active"))
	assert.True(t, schemas.IsValidCanaryStatus("expired"))
	assert.False(t, schemas.IsValidCanaryStatus("pending"))
	assert.True(t, schemas.IsValidShadowDisagreementResolution("both_wrong"))
	assert.False(t, schemas.IsValidShadowDisagreementResolution("candidate_won"))
	assert.True(t, schemas.IsValidShadowSampler("sha256-hex-threshold/v1"))
	assert.False(t, schemas.IsValidShadowSampler("random/v1"))

	// The sha256-hex shapes: SHADOW_INPUT_HASH_REGEX and SHA256_HASH_REGEX.
	assert.True(t, schemas.IsShadowInputHash(strings.Repeat("a", 64)))
	assert.False(t, schemas.IsShadowInputHash("8a73f2c"))
	assert.False(t, schemas.IsShadowInputHash(strings.Repeat("A", 64)))
	assert.True(t, schemas.IsSHA256HashDigest("sha256:"+strings.Repeat("a", 64)))
	assert.False(t, schemas.IsSHA256HashDigest(strings.Repeat("a", 64)))
	assert.False(t, schemas.IsSHA256HashDigest("sha256:"+strings.Repeat("a", 63)))
	assert.False(t, schemas.IsSHA256HashDigest("sha256:"+strings.Repeat("A", 64)))
}

func TestPhase12SchemasNestedBlocksAreValidatedWhenPresent(t *testing.T) {
	// A promotion evaluation must carry its report (z.record(z.unknown())).
	record := schemas.PromotionRecordExample()
	record.Evaluation.Report = nil
	require.NotEmpty(t, record.Validate())
	record = schemas.PromotionRecordExample()
	record.PreviousVersion = new(0)
	refusedWith(t, wireOf(t, record, nil), func(b []byte) error {
		_, err := schemas.ParsePromotionRecord(b)
		return err
	}, "previous_version: Number must be greater than 0")

	// Shadow sampling bounds: the fraction is versioned policy data in [0, 1].
	shadowRun := schemas.ShadowRunRecordExample()
	shadowRun.Sampling.Fraction = -0.1
	require.NotEmpty(t, shadowRun.Validate())

	// Every recorded input hash is sha256 hex (V-P12-2 lineage).
	shadowRun = schemas.ShadowRunRecordExample()
	shadowRun.InputHashes.CandidateSet = "8a73f2c"
	refusedWith(t, wireOf(t, shadowRun, nil), func(b []byte) error {
		_, err := schemas.ParseShadowRunRecord(b)
		return err
	}, "candidate_set: Invalid")
	shadowRun = schemas.ShadowRunRecordExample()
	shadowRun.InputHashes.DiffSubset = "8a73f2c"
	refusedWith(t, wireOf(t, shadowRun, nil), func(b []byte) error {
		_, err := schemas.ParseShadowRunRecord(b)
		return err
	}, "diff_subset: Invalid")

	// A recorded shadow decision still carries the decision vocabulary.
	shadowRun = schemas.ShadowRunRecordExample()
	shadowRun.Decision.AbstainReason = new(schemas.AbstainReason("not_a_reason"))
	refusedWith(t, wireOf(t, shadowRun, nil), func(b []byte) error {
		_, err := schemas.ParseShadowRunRecord(b)
		return err
	}, "abstain_reason: Invalid enum value. Expected 'insufficient_confidence' | 'conflicting_evidence' | 'policy_gap' | 'calibration_floor' | 'verifier_uncertain' | 'schema_violation' | 'model_unavailable' | 'cap_exceeded' | 'policy_missing' | 'other', received 'not_a_reason'")

	// Threshold rows are validated per row (case_count, unit interval).
	calibration := schemas.ThresholdCalibrationExample()
	calibration.TimeSplitReport.Splits[1].CaseCount = 0
	require.NotEmpty(t, calibration.Validate())
	calibration = schemas.ThresholdCalibrationExample()
	calibration.RollingReevalReport.Windows[1].HighConfidenceShare = 1.5
	require.NotEmpty(t, calibration.Validate())
}
