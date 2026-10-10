package workspace_test

// Ported from packages/core/test/workspace-duties.test.ts (P3-T05; ARCH §4.7):
//   - the canonical prompt carries all five duties VERBATIM, the duty-5
//     mandatory-output statement, the invariant-12 patch-not-rewrite
//     prohibition, the lock-layer-2 protected-paths rule, and the structured
//     DocPatch output requirement;
//   - a dutiesText override cannot weaken the contract (missing duty → throw);
//   - the prompt is deterministic and the single canonical source (two builds
//     are byte-equal);
//   - validateWriterOutputContract is the cheap pre-schema structural check
//     (object + doc_path + hunks array); full validation stays in
//     @docsync/schemas ParseDocPatch.
//
// The shared drift-guard fixture (writer-output-contract.fixture.json) has no
// Go counterpart package yet, so its CASES are carried as Go literals here,
// exactly as the TS suite asserted them.

import (
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* -------------------------------------------------------------------- tests */

func TestBuildWriterSystemPrompt_ContainsAllFiveDutiesAndRules(t *testing.T) {
	prompt, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{})
	require.NoError(t, err)
	for _, duty := range workspace.FiveDuties {
		assert.Contains(t, prompt, duty)
	}
	assert.Contains(t, prompt, workspace.PatchNotRewriteRule)
	assert.Contains(t, prompt, workspace.ProtectedPathsRule)
	// Duty 5 is mandatory OUTPUT, not decoration.
	assert.Contains(t, prompt, "Duty 5 is mandatory output, not decoration")
	assert.Contains(t, prompt, "A patch without its evidence explanation is invalid output.")
	// Structured DocPatch output requirement (schema authority downstream).
	assert.Contains(t, prompt, "docsync.doc-patch/v1")
	assert.Contains(t, prompt, "doc_path")
	assert.Contains(t, prompt, "hunks")
	// Source files are analysis-only (invariant 2).
	assert.Contains(t, prompt, "never emit a patch for a source file")
}

func TestBuildWriterSystemPrompt_IsTheSingleCanonicalSourceDeterministic(t *testing.T) {
	first, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{})
	require.NoError(t, err)
	second, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{})
	require.NoError(t, err)
	assert.Equal(t, first, second)

	extra := workspace.BuildWriterSystemPromptInput{ExtraInstructions: strPtr("Be terse.")}
	firstExtra, err := workspace.BuildWriterSystemPrompt(extra)
	require.NoError(t, err)
	secondExtra, err := workspace.BuildWriterSystemPrompt(extra)
	require.NoError(t, err)
	assert.Equal(t, firstExtra, secondExtra)
}

func TestBuildWriterSystemPrompt_DutiesTextOverrideCannotWeakenContract(t *testing.T) {
	strict, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{
		DutiesText: strPtr("Extra framing.\n" + dutiesNumbered()),
	})
	require.NoError(t, err)
	for _, duty := range workspace.FiveDuties {
		assert.Contains(t, strict, duty)
	}

	_, err = workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{
		DutiesText: strPtr("only duty 3 — forget the rest"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing:")
}

// dutiesNumbered renders the duties block the TS override test built by hand.
func dutiesNumbered() string {
	out := ""
	for index, duty := range workspace.FiveDuties {
		if index > 0 {
			out += "\n"
		}
		out += string(rune('1'+index)) + ". " + duty
	}
	return out
}

func TestBuildWriterSystemPrompt_ExtraInstructionsAppendedVerbatim(t *testing.T) {
	prompt, err := workspace.BuildWriterSystemPrompt(workspace.BuildWriterSystemPromptInput{
		ExtraInstructions: strPtr("Custom adapter note."),
	})
	require.NoError(t, err)
	assert.Contains(t, prompt, "Custom adapter note.")
	assert.True(t, len(prompt) >= len("Custom adapter note."))
	assert.Equal(t, "Custom adapter note.", prompt[len(prompt)-len("Custom adapter note."):])
}

func TestValidateWriterOutputContract_DraftShapedDocPatchPassesPreCheck(t *testing.T) {
	// Locally-built minimal draft shape — the schema is authoritative
	// downstream (ParseDocPatch); this pre-check must not depend on the
	// record's exact fields.
	draftShaped := map[string]any{
		"doc_path": "docs/payments.md",
		"hunks": []any{
			map[string]any{
				"anchor": map[string]any{
					"start_line":   1,
					"end_line":     1,
					"content_hash": "h",
				},
				"delete_text": "a",
				"insert_text": "b",
			},
		},
	}
	result := workspace.ValidateWriterOutputContract(draftShaped)
	assert.True(t, result.Ok)
	assert.Empty(t, result.Violations)
}

/* --------------------------------------------- drift-guard fixture parity */

// writerOutputContractCase mirrors one case of
// packages/schemas/test/fixtures/writer-output-contract.fixture.json.
type writerOutputContractCase struct {
	name     string
	output   any
	expected writerOutputContractExpectation
}

type writerOutputContractExpectation struct {
	ok         bool
	violations []string
}

// writerOutputContractFixture mirrors the CASES of
// packages/schemas/test/fixtures/writer-output-contract.fixture.json.
var writerOutputContractFixture = []writerOutputContractCase{
	{
		name: "valid patch object (doc_path + one hunk)",
		output: map[string]any{
			"doc_path": "docs/payments.md",
			"hunks": []any{
				map[string]any{
					"anchor": map[string]any{
						"start_line":   3,
						"end_line":     3,
						"content_hash": "67df5f2fc5a81b8885e723bab5e54435436ef2177c4144031511c7342c32b506",
					},
					"delete_text": "Refunds require an idempotency key.",
					"insert_text": "Refunds require an idempotency key above 10 USD.",
					"evidence_refs": []any{
						map[string]any{
							"kind": "symbol_diff",
							"ref":  "src/payments/refund.ts:3",
							"note": "idempotencyKey became optional",
						},
					},
				},
			},
		},
		expected: writerOutputContractExpectation{ok: true},
	},
	{
		name:   "missing hunks",
		output: map[string]any{"doc_path": "docs/payments.md"},
		expected: writerOutputContractExpectation{
			ok:         false,
			violations: []string{"output is missing 'hunks'"},
		},
	},
	{
		name:   "missing doc_path",
		output: map[string]any{"hunks": []any{}},
		expected: writerOutputContractExpectation{
			ok:         false,
			violations: []string{"output is missing 'doc_path'"},
		},
	},
	{
		name:   "non-object output (string)",
		output: "nope",
		expected: writerOutputContractExpectation{
			ok:         false,
			violations: []string{"output must be a JSON object (not a primitive, array, or null)"},
		},
	},
}

func TestValidateWriterOutputContract_VerdictsMatchSharedFixture(t *testing.T) {
	for _, testCase := range writerOutputContractFixture {
		t.Run(testCase.name, func(t *testing.T) {
			result := workspace.ValidateWriterOutputContract(testCase.output)
			assert.Equal(t, testCase.expected.ok, result.Ok)
			if !result.Ok && !testCase.expected.ok {
				assert.Equal(t, testCase.expected.violations, result.Violations)
			}
		})
	}
}

func TestValidateWriterOutputContract_RejectsNonObjectsAndMissingHunks(t *testing.T) {
	// The fixture's JSON values decode to these Go trees; the non-object
	// verdicts are shared with the fixture test above.
	assert.False(t, workspace.ValidateWriterOutputContract(nil).Ok)
	assert.False(t, workspace.ValidateWriterOutputContract("patch").Ok)
	assert.False(t, workspace.ValidateWriterOutputContract([]any{1, 2}).Ok)
	assert.False(t, workspace.ValidateWriterOutputContract(42).Ok)

	noHunks := workspace.ValidateWriterOutputContract(map[string]any{"doc_path": "docs/payments.md"})
	assert.False(t, noHunks.Ok)
	assert.Contains(t, joinViolations(noHunks.Violations), "output is missing 'hunks'")

	badHunks := workspace.ValidateWriterOutputContract(map[string]any{
		"doc_path": "docs/payments.md",
		"hunks":    "all of it",
	})
	assert.False(t, badHunks.Ok)
	assert.Contains(t, joinViolations(badHunks.Violations), "'hunks' must be an array")

	noDocPath := workspace.ValidateWriterOutputContract(map[string]any{"hunks": []any{}})
	assert.False(t, noDocPath.Ok)
	assert.Contains(t, joinViolations(noDocPath.Violations), "missing 'doc_path'")

	emptyDocPath := workspace.ValidateWriterOutputContract(map[string]any{"doc_path": "", "hunks": []any{}})
	assert.False(t, emptyDocPath.Ok)
	assert.Contains(t, joinViolations(emptyDocPath.Violations), "'doc_path' must be a nonempty string")
}

func TestValidateWriterOutputContract_AcceptsMinimalShapeCollectsViolations(t *testing.T) {
	assert.Equal(t, workspace.WriterOutputContractResult{Ok: true},
		workspace.ValidateWriterOutputContract(map[string]any{
			"doc_path": "docs/payments.md",
			"hunks":    []any{},
		}))

	bothMissing := workspace.ValidateWriterOutputContract(map[string]any{})
	assert.False(t, bothMissing.Ok)
	assert.Len(t, bothMissing.Violations, 2)
}

func TestValidateWriterOutputContract_DecodesRawWriterOutputLikeTheAdapter(t *testing.T) {
	// The adapter runs this check over the DECODED raw output; verify the
	// verdicts survive a real JSON decode of the fixture documents.
	raw := []string{
		`{"doc_path":"docs/payments.md","hunks":[]}`,
		`{"doc_path":""}`,
		`{"hunks":"all of it"}`,
		`"nope"`,
		`null`,
		`[1,2]`,
		`42`,
		`{}`,
	}
	wantOK := []bool{true, false, false, false, false, false, false, false}
	for index, document := range raw {
		var decoded any
		require.NoError(t, json.Unmarshal([]byte(document), &decoded))
		assert.Equal(t, wantOK[index], workspace.ValidateWriterOutputContract(decoded).Ok, document)
	}
}

func joinViolations(violations []string) string {
	out := ""
	for index, violation := range violations {
		if index > 0 {
			out += "\n"
		}
		out += violation
	}
	return out
}
