// Ported from packages/policy/test/policy-versioning.test.ts — policy-history
// versioning, rollback-as-metadata and the unsafe-auto-updates promotion gate
// (ARCH §8.4; OD-R04/R4). Black-box: package policy_test.
package policy_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// record builds one history record (TS: record()).
func record(version int, previous *string) policy.PolicyVersionRecord {
	return policy.PolicyVersionRecord{
		PolicyVersion: version,
		CreatedBy:     "bootstrap",
		Reason:        fmt.Sprintf("Policy version %d", version),
		CreatedAt:     "2026-01-01T00:00:00.000Z",
		Previous:      previous,
		PolicyHash:    fmt.Sprintf("sha256:%064d", version),
	}
}

func stringPointer(value string) *string { return &value }

// chain is the canonical three-record history (TS: chain()).
func chain() []policy.PolicyVersionRecord {
	return []policy.PolicyVersionRecord{
		record(1, nil),
		record(2, stringPointer("policy-history/v1.yml")),
		record(3, stringPointer("policy-history/v2.yml")),
	}
}

func findDiagnostic(diagnostics []policy.PolicyDiagnostic, kind string) *policy.PolicyDiagnostic {
	for index := range diagnostics {
		if diagnostics[index].Kind == kind {
			return &diagnostics[index]
		}
	}
	return nil
}

// findDiagnosticAtPath finds the diagnostic with both the kind and the path.
func findDiagnosticAtPath(diagnostics []policy.PolicyDiagnostic, kind, path string) *policy.PolicyDiagnostic {
	for index := range diagnostics {
		if diagnostics[index].Kind == kind && diagnostics[index].Path == path {
			return &diagnostics[index]
		}
	}
	return nil
}

func TestPolicyHistoryChainIntegrity(t *testing.T) {
	t.Run("accepts a strictly increasing chain with intact previous pointers", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{Records: chain()})
		require.True(t, result.OK)
		require.NotNil(t, result.History)
		assert.Equal(t, 3, result.History.ActiveVersion)
		assert.Len(t, result.History.Records, 3)
		assert.Equal(t, policy.PolicyHistoryDefaultDir, result.History.StorageDir)
	})

	t.Run("honors a custom storageDir without touching the pointer layout", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records:    chain(),
			StorageDir: ".docsync/state",
		})
		require.True(t, result.OK)
		require.NotNil(t, result.History)
		assert.Equal(t, ".docsync/state", result.History.StorageDir)
	})

	t.Run("rejects decreasing versions", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{
				record(2, nil),
				record(1, stringPointer("policy-history/v2.yml")),
			},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "version_order_violation")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.1.policy_version", diagnostic.Path)
	})

	t.Run("rejects duplicate versions", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{
				record(1, nil),
				record(1, stringPointer("policy-history/v1.yml")),
			},
		})
		require.False(t, result.OK)
		assert.NotNil(t, findDiagnostic(result.Diagnostics, "duplicate_version"))
	})

	t.Run("rejects a first record with a non-null previous pointer", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{record(1, stringPointer("policy-history/v0.yml"))},
		})
		require.False(t, result.OK)
		assert.NotNil(t, findDiagnostic(result.Diagnostics, "previous_pointer_violation"))
	})

	t.Run("rejects a previous pointer that misses the prior version record", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{
				record(1, nil),
				record(2, stringPointer("policy-history/v17.yml")),
			},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "previous_pointer_violation")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.1.previous", diagnostic.Path)
		assert.Contains(t, diagnostic.Message, "policy-history/v1.yml")
	})

	t.Run("rejects malformed records with record_invalid paths", func(t *testing.T) {
		malformed := record(1, nil)
		malformed.PolicyVersion = 0
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{malformed},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "record_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.0.policy_version", diagnostic.Path)
	})

	t.Run("rejects empty history (bootstrap v1 first)", func(t *testing.T) {
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{},
		})
		require.False(t, result.OK)
		require.NotEmpty(t, result.Diagnostics)
		assert.Equal(t, "record_invalid", result.Diagnostics[0].Kind)
	})
}

func TestRollbackAsMetadata(t *testing.T) {
	t.Run("promotes a prior version by moving the live pointer only", func(t *testing.T) {
		result := policy.RollbackTo(chain(), 1)
		require.True(t, result.OK)
		assert.Equal(t, 1, result.ActiveVersion)
		require.NotNil(t, result.PointerRecord)
		assert.Equal(t, schemas.PolicyPointer{
			Schema:  "docsync.policy/v1",
			Version: 1,
			Pointer: "policy-history/v1.yml",
			Hash:    record(1, nil).PolicyHash,
		}, *result.PointerRecord)

		// The pointer payload MUST satisfy the registered docsync.policy/v1
		// artifact schema (OD-R04).
		wire, err := json.Marshal(result.PointerRecord)
		require.NoError(t, err)
		parsed, err := schemas.ParsePolicyPointer(wire)
		require.NoError(t, err)
		require.NotNil(t, parsed)
	})

	t.Run("produces a pointer payload matching the registered docsync.policy/v1 artifact for any version", func(t *testing.T) {
		result := policy.RollbackTo(chain(), 3)
		require.True(t, result.OK)
		require.NotNil(t, result.PointerRecord)
		assert.Equal(t, "policy-history/v3.yml", result.PointerRecord.Pointer)
		assert.Equal(t, record(3, stringPointer("policy-history/v2.yml")).PolicyHash, result.PointerRecord.Hash)
	})

	t.Run("rejects rollback to an absent version (fail-closed)", func(t *testing.T) {
		result := policy.RollbackTo(chain(), 9)
		require.False(t, result.OK)
		require.NotEmpty(t, result.Diagnostics)
		assert.Equal(t, "version_not_found", result.Diagnostics[0].Kind)
		assert.Contains(t, result.Diagnostics[0].Message, "9")
	})

	t.Run("refuses to operate on a broken history (no partial rollback)", func(t *testing.T) {
		result := policy.RollbackTo(
			[]policy.PolicyVersionRecord{record(2, stringPointer("policy-history/v17.yml"))}, 2)
		require.False(t, result.OK)
		assert.NotNil(t, findDiagnostic(result.Diagnostics, "previous_pointer_violation"))
	})
}

func TestPolicyHistoryStorageLayout(t *testing.T) {
	t.Run("names records policy-history/v<N>.<ext>, matching the pointer artifact example", func(t *testing.T) {
		path, err := policy.PolicyHistoryPath(17)
		require.NoError(t, err)
		assert.Equal(t, "policy-history/v17.yml", path)
		// The registered docsync.policy/v1 pointer example's `pointer` field
		// is exactly this layout (internal/schemas state.ts POLICY_EXAMPLE).
		assert.Equal(t, "policy-history/v17.yml", path)

		path, err = policy.PolicyHistoryPath(2, "json")
		require.NoError(t, err)
		assert.Equal(t, "policy-history/v2.json", path)
	})

	t.Run("rejects non-positive versions (programmer error)", func(t *testing.T) {
		// The TS original threw a RangeError; the Go port returns a coded
		// error (docutil.CodeValidation) with the same message text.
		_, err := policy.PolicyHistoryPath(0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyHistoryPath requires a positive integer version")

		_, err = policy.PolicyHistoryPath(-1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "policyHistoryPath requires a positive integer version")
	})
}

func TestUnsafeAutoUpdatesGate(t *testing.T) {
	t.Run("a clean history promotes", func(t *testing.T) {
		assert.Equal(t, policy.UnsafeAutoUpdatesGateResult{
			UnsafeAutoUpdates: 0,
			PromotionAllowed:  true,
		}, policy.UnsafeAutoUpdatesGate(chain()))
	})

	t.Run("counts a record whose struct enables unsafe auto updates (enabled !== false)", func(t *testing.T) {
		violating := record(2, stringPointer("policy-history/v1.yml"))
		violating.UnsafeAutoUpdates = &policy.UnsafeAutoUpdates{Enabled: true}
		assert.Equal(t, policy.UnsafeAutoUpdatesGateResult{
			UnsafeAutoUpdates: 1,
			PromotionAllowed:  false,
		}, policy.UnsafeAutoUpdatesGate([]policy.PolicyVersionRecord{record(1, nil), violating}))
	})

	t.Run("counts a record whose evaluation metric shows unsafe auto updates (> 0)", func(t *testing.T) {
		violating := record(2, stringPointer("policy-history/v1.yml"))
		violating.Evaluation = &policy.PolicyEvaluation{
			Split: "evaluation/bad-split",
			Metrics: map[string]float64{
				"precision":           0.99,
				"unsafe_auto_updates": 1,
			},
		}
		assert.Equal(t, policy.UnsafeAutoUpdatesGateResult{
			UnsafeAutoUpdates: 1,
			PromotionAllowed:  false,
		}, policy.UnsafeAutoUpdatesGate([]policy.PolicyVersionRecord{record(1, nil), violating}))
	})

	t.Run("accepts enabled: false structs and zero metrics", func(t *testing.T) {
		clean := record(2, stringPointer("policy-history/v1.yml"))
		clean.UnsafeAutoUpdates = &policy.UnsafeAutoUpdates{Enabled: false}
		clean.Evaluation = &policy.PolicyEvaluation{
			Split: "evaluation/split",
			Metrics: map[string]float64{
				"precision":           0.96,
				"unsafe_auto_updates": 0,
			},
		}
		assert.Equal(t, policy.UnsafeAutoUpdatesGateResult{
			UnsafeAutoUpdates: 0,
			PromotionAllowed:  true,
		}, policy.UnsafeAutoUpdatesGate([]policy.PolicyVersionRecord{clean}))
	})
}

func TestPolicyHistoryRecordShapeDiagnostics(t *testing.T) {
	t.Run("rejects empty required string fields with record_invalid paths", func(t *testing.T) {
		broken := record(1, nil)
		broken.CreatedBy = ""
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{broken},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "record_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.0.created_by", diagnostic.Path)
		assert.Equal(t, "created_by must be a non-empty string", diagnostic.Message)
	})

	t.Run("rejects an empty previous pointer string", func(t *testing.T) {
		broken := record(2, stringPointer(""))
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{record(1, nil), broken},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "record_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.1.previous", diagnostic.Path)
		assert.Equal(t, "previous must be null or a non-empty pointer string", diagnostic.Message)
	})

	t.Run("rejects an empty evaluation split", func(t *testing.T) {
		broken := record(2, stringPointer("policy-history/v1.yml"))
		broken.Evaluation = &policy.PolicyEvaluation{Split: "", Metrics: map[string]float64{"precision": 1}}
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{record(1, nil), broken},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "record_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.1.evaluation.split", diagnostic.Path)
		assert.Equal(t, "evaluation.split must be a non-empty string", diagnostic.Message)
	})

	t.Run("rejects non-finite evaluation metrics", func(t *testing.T) {
		broken := record(2, stringPointer("policy-history/v1.yml"))
		broken.Evaluation = &policy.PolicyEvaluation{
			Split:   "evaluation/split",
			Metrics: map[string]float64{"unsafe_auto_updates": math.Inf(1)},
		}
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{record(1, nil), broken},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnostic(result.Diagnostics, "record_invalid")
		require.NotNil(t, diagnostic)
		assert.Equal(t, "records.1.evaluation.metrics.unsafe_auto_updates", diagnostic.Path)
		assert.Equal(t, "evaluation metrics must be finite numbers", diagnostic.Message)
	})

	t.Run("reports an undecidable prior version as a record_invalid (the TS RangeError catch)", func(t *testing.T) {
		// A prior record with a non-positive version cannot yield a pointer
		// path; the TS policyHistoryPath threw a RangeError here and the
		// validator caught it into a record_invalid diagnostic.
		result := policy.BuildPolicyHistory(policy.BuildPolicyHistoryInput{
			Records: []policy.PolicyVersionRecord{record(0, nil), record(1, stringPointer("policy-history/v0.yml"))},
		})
		require.False(t, result.OK)
		diagnostic := findDiagnosticAtPath(result.Diagnostics, "record_invalid", "records.1")
		require.NotNil(t, diagnostic)
		assert.Contains(t, diagnostic.Message,
			"policyHistoryPath requires a positive integer version (got 0)")
		// The malformed prior record is itself reported.
		assert.NotNil(t, findDiagnosticAtPath(result.Diagnostics, "record_invalid", "records.0.policy_version"))
	})
}
