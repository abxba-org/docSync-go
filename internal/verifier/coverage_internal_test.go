package verifier

// Internal coverage for the Tier-1/2/3 pure helpers whose flat branches are
// only reachable through direct calls (line counts, split/patched lines,
// binding mode rendering, evidence windows, contract graphs, formatting).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

func iPtr(v int) *int { return &v }

func TestCOV_INTERNAL_VERIFIER_tier2LineAndPatchHelpers(t *testing.T) {
	assert.Equal(t, 0, tier2InsertLineCount(""))
	assert.Equal(t, 2, tier2InsertLineCount("a\nb\n"))

	assert.Equal(t, []string{"a", "b"}, tier2SplitInsertLines("a\nb\n"))
	assert.Equal(t, []string{""}, tier2SplitInsertLines(""))
	assert.Equal(t, []string{"a"}, tier2SplitInsertLines("a\n"))

	hunk := &schemas.PatchHunk{InsertText: "x"}
	line := tier2PatchedLine(hunk, 3)
	require.NotNil(t, line)
	assert.Equal(t, 3, *line) // zero anchor => start_line 0 + offset 3
	assert.Nil(t, tier2PatchedLine(&schemas.PatchHunk{}, 1))
}

func TestCOV_INTERNAL_VERIFIER_furnitureAndLineKinds(t *testing.T) {
	assert.True(t, tier2IsFurniture(""))
	assert.True(t, tier2IsFurniture("   "))
	assert.True(t, tier2IsFurniture("```ts"))
	assert.True(t, tier2IsFurniture("## Heading"))
	assert.True(t, tier2IsFurniture("- "))
	assert.True(t, tier2IsFurniture("| - | : |"))
	assert.False(t, tier2IsFurniture("content"))

	assert.Equal(t, DecomposedClaimKindErrorCode, tier2ClassifyLine("note `E_HTTP_400` fires"))
	assert.Equal(t, DecomposedClaimKindErrorCode, tier2ClassifyLine("returns 500 on demand"))
	assert.Equal(t, DecomposedClaimKindSignature, tier2ClassifyLine("- user `refundPayment()` returns"))
	assert.Equal(t, DecomposedClaimKindValue, tier2ClassifyLine("the API returns `0.94`"))
	assert.Equal(t, DecomposedClaimKindBehavioral, tier2ClassifyLine("- callers observe idempotent retries"))
	assert.Equal(t, DecomposedClaimKindOther, tier2ClassifyLine("plain prose line"))

	// nil patch decomposes to no claims; claim_ids attach to the hunk's claims.
	assert.Empty(t, DecomposePatchClaims(nil))
	decomposed := DecomposePatchClaims(&schemas.DocPatch{
		Hunks: []schemas.PatchHunk{
			{InsertText: "line one\nline two\n", ClaimIDs: []string{"c1"}},
		},
	})
	require.Len(t, decomposed, 2)
	assert.Equal(t, "c1", decomposed[0].ExistingClaimID)
	assert.Zero(t, decomposed[0].Index)
	assert.Equal(t, 1, decomposed[1].Index)
}

func TestCOV_INTERNAL_VERIFIER_tokenMatchingAndWorkspaceSearch(t *testing.T) {
	assert.True(t, tier2TokenMatchesLine("foo", DecomposedClaimKindOther, "a foo b"))
	assert.False(t, tier2TokenMatchesLine("foo", DecomposedClaimKindOther, "no match"))
	assert.True(t, tier2TokenMatchesLine("refund Payment ()", DecomposedClaimKindSignature, "refundPayment()"))
	assert.True(t, tier2TokenMatchesLine("refundPayment(x)", DecomposedClaimKindSignature, "calls refundPayment all day"))
	assert.False(t, tier2TokenMatchesLine("zzz", DecomposedClaimKindSignature, "line"))

	binding := tier2SearchWorkspaceSources(nil, DecomposedClaimKindValue, []string{"a.ts"}, map[string]string{})
	assert.False(t, binding.Bound)
	binding = tier2SearchWorkspaceSources([]string{"tok"}, DecomposedClaimKindValue, []string{"a.ts"}, map[string]string{})
	assert.False(t, binding.Bound)
	binding = tier2SearchWorkspaceSources(
		[]string{"tok"}, DecomposedClaimKindValue,
		[]string{"b.ts", "a.ts"},
		map[string]string{"b.ts": "line tok here", "a.ts": "other"},
	)
	require.True(t, binding.Bound)
	assert.Equal(t, "b.ts", binding.File)
	require.NotNil(t, binding.Line)
	assert.Equal(t, 1, *binding.Line)
}

func TestCOV_INTERNAL_VERIFIER_bindingDetailAndInsertCounts(t *testing.T) {
	bindings := []ClaimBinding{
		{Index: 0, Bound: false, Via: BindingViaNone},
		{Index: 1, Bound: true, Via: BindingViaClaimStore},
		{Index: 2, Bound: true, Via: BindingViaWorkspaceToken},
	}
	detail := tier2BindingDetail(bindings)
	assert.Contains(t, detail, "2 bound (1 claim-store, 1 workspace-token), 1 unanchored")
	assert.Contains(t, detail, "declared V1 gap")

	noToken := tier2BindingDetail([]ClaimBinding{{Index: 0, Bound: true, Via: BindingViaClaimStore}})
	assert.NotContains(t, noToken, "declared V1 gap")
}

func TestCOV_INTERNAL_VERIFIER_evidenceWindowAndHeadVerify(t *testing.T) {
	content := "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12\nl13\nl14\nl15\nl16\nl17\nl18\nl19\nl20\nl21\nl22\nl23\nl24\nl25\nl26\nl27\nl28\nl29\nl30\nl31\nl32\n"
	whole := tier2EvidenceWindow(content, nil)
	assert.Equal(t, "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12\nl13\nl14\nl15\nl16\nl17\nl18\nl19\nl20\nl21\nl22\nl23\nl24\nl25\nl26\nl27\nl28\nl29\nl30\nl31\nl32", whole)

	// Near the top: the window clamps to the first line.
	top := tier2EvidenceWindow(content, iPtr(2))
	assert.Equal(t, "l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\nl11\nl12", top)

	// Beyond the file: a stale anchor is a caller bug; well-formed bindings
	// always stay in-range (the window clamp touches both rails).
	bottom := tier2EvidenceWindow(content, iPtr(31))
	assert.Equal(t, "l21\nl22\nl23\nl24\nl25\nl26\nl27\nl28\nl29\nl30\nl31\nl32", bottom)
}

func TestCOV_INTERNAL_VERIFIER_contractAndCoverageHelpers(t *testing.T) {
	assert.True(t, tier2SharesWord([]string{"a", "b"}, []string{"b"}))
	assert.False(t, tier2SharesWord([]string{"a"}, []string{"b"}))
	assert.False(t, tier2SharesWord(nil, []string{"a"}))

	assert.Nil(t, tier2ContractsGraph(nil))
	g := &graph.DocumentGraph{
		Mappings: []graph.MappingRecord{{Doc: "d", Sources: []string{"s"}, Contracts: []string{"c"}}},
		Errors:   []graph.GraphConfigError{{Kind: "k", Path: "p", Message: "m"}},
	}
	contractGraph := tier2ContractsGraph(g)
	require.NotNil(t, contractGraph)
	require.Len(t, contractGraph.Errors, 1)
	assert.Equal(t, "k", string(contractGraph.Errors[0].Kind))
}

func TestCOV_INTERNAL_VERIFIER_tier3AndFormattingHelpers(t *testing.T) {
	assert.Equal(t, "", abstainReasonText(nil))
	reason := schemas.AbstainReason("model-unavailable")
	assert.Equal(t, "model-unavailable", abstainReasonText(&reason))

	assert.Equal(t, "(no triggering diff — not measurable)", tier1FormatRatio(nil))
	assert.Equal(t, "1.5", tier1FormatRatio(f64Ptr(1.5)))
	assert.Equal(t, "(root): nope", formatIssues([]schemas.Issue{{Path: []string{}, Message: "nope"}}))
	assert.Equal(t, "a.b: c", formatIssues([]schemas.Issue{{Path: []string{"a", "b"}, Message: "c"}}))

	// contentOnlyGit refuses every read (the fail-closed stub).
	_, err := (contentOnlyGit{}).CatFile(t.Context(), "rev", "path")
	require.Error(t, err)
}

// f64Ptr returns a *float64.
func f64Ptr(v float64) *float64 { return &v }

/* --------------------------------------------- replay audit + storage */

func TestCOV_INTERNAL_VERIFIER_replayAuditHelpers(t *testing.T) {
	dir := t.TempDir()

	dirAbsent := filepath.Join(dir, "gone")
	names, err := listAuditJSONFiles(dirAbsent)
	require.NoError(t, err)
	assert.Empty(t, names)

	auditDir := filepath.Join(dir, "docs")
	require.NoError(t, os.MkdirAll(auditDir, 0o755))
	corrupt := filepath.Join(auditDir, "corrupt.json")
	require.NoError(t, os.WriteFile(corrupt, []byte("{not json"), 0o644))
	nonObject := filepath.Join(auditDir, "nonobject.json")
	require.NoError(t, os.WriteFile(nonObject, []byte("[]"), 0o644))

	record, ok := readAuditRecord(corrupt)
	assert.False(t, ok)
	assert.Nil(t, record.value)
	record, ok = readAuditRecord(nonObject)
	assert.False(t, ok)
	assert.Nil(t, record.value)
	record, ok = readAuditRecord(filepath.Join(auditDir, "missing.json"))
	assert.False(t, ok)
	assert.Nil(t, record.value)
	good := filepath.Join(auditDir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"run_id":"r"}`), 0o644))
	record, ok = readAuditRecord(good)
	assert.True(t, ok)
	assert.Equal(t, "r", record.value["run_id"])

	assert.Nil(t, auditLedgerVerbatim(json.RawMessage(`{"verifications":`)))
	ledger := auditLedgerVerbatim(json.RawMessage(`{"verifications":[{"report":{},"run_id":"r"}]}`))
	require.Len(t, ledger, 1)

	assert.Equal(t, "", auditRunIDOf(map[string]any{}))
	assert.Equal(t, "", auditRunIDOf(map[string]any{"run_id": ""}))
	assert.Equal(t, "", auditRunIDOf(map[string]any{"run_id": 42}))
	assert.Equal(t, "r7", auditRunIDOf(map[string]any{"run_id": "r7"}))

	assert.Equal(t, "unknown", stringifyVerbatim(nil))
	assert.Equal(t, "unknown", stringifyVerbatim(json.RawMessage("null")))
	assert.Equal(t, "hello", stringifyVerbatim(json.RawMessage(`"hello"`)))
	assert.Equal(t, "42", stringifyVerbatim(json.RawMessage("42")))

	assert.True(t, hasIncidentsFileGap([]string{"no incidents.jsonl found under state"}))
	assert.False(t, hasIncidentsFileGap([]string{"something else"}))

	pretty := auditPrettyJSON(json.RawMessage(`{"a":1}`))
	assert.Equal(t, "{\n  \"a\": 1\n}", pretty)
	assert.Equal(t, "not json", auditPrettyJSON(json.RawMessage("not json")))
	assert.Equal(t, "null", auditCompactJSON(nil))
	assert.Equal(t, `{"a":1}`, auditCompactJSON(json.RawMessage("{\n  \"a\": 1\n}")))
	assert.Equal(t, "not json", auditCompactJSON(json.RawMessage("not json")))
}

func TestCOV_INTERNAL_VERIFIER_storageHashHelpers(t *testing.T) {
	assert.Equal(t, "null", storageHashOrNull(nil))
	hash := "abc"
	assert.Equal(t, "abc", storageHashOrNull(&hash))
	assert.Equal(t, "abc", *storageStringPtr("abc"))

	msg := casConflictMessage("verification record", "docs/x.md", nil, nil)
	assert.Contains(t, msg, "expected null, actual null")

	_, err := canonicalValueBytes(make(chan int))
	require.Error(t, err)
	canonical, err := canonicalValueBytes(map[string]any{"a": 1})
	require.NoError(t, err)
	assert.Contains(t, canonical, "1")
}

func TestCOV_INTERNAL_VERIFIER_pipelineHelpers(t *testing.T) {
	// declaredVerifiedClaimRefs dedupes patch-level and per-hunk refs.
	refs := declaredVerifiedClaimRefs(&schemas.DocPatch{
		VerifiedClaims: []string{"c1", "c1", "c2"},
		Hunks:          []schemas.PatchHunk{{VerifiedClaimRefs: []string{"c2", "c3"}}, {VerifiedClaimRefs: []string{"c2"}}},
	})
	assert.Equal(t, []string{"c1", "c2", "c3"}, refs)
	refs = declaredVerifiedClaimRefs(&schemas.DocPatch{})
	assert.Empty(t, refs)

	// computePatchRef's route refusal surfaces.
	_, err := ComputePatchRef(&schemas.DocPatch{DocPath: "../escape.md"})
	require.Error(t, err)
}
