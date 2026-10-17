package verifier_test

// Public-surface coverage for the remaining Tier-1/2 pipeline branches:
// CLI doc scoping + runtime failure classification, the head-verification
// branch matrix, the over-deletion grading loop, and cross-patch no-ops.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCOV_VERIFIER_BinDocScopeAndRuntimeFailure(t *testing.T) {
	root := seedStateBinCov(t)
	var out, errOut strings.Builder
	io := verifier.AuditReplayCLIIo{
		Stdout: func(line string) { out.WriteString(line + "\n") },
		Stderr: func(line string) { errOut.WriteString(line + "\n") },
	}
	code := verifier.RunAuditReplayCLI([]string{
		"--state-dir", root, "--run", auditRunID, "--doc", "docs/payments.md",
	}, io)
	assert.Equal(t, verifier.AuditCLIExitOK, code)
	assert.Contains(t, out.String(), "docs/payments.md")

	out.Reset()
	errOut.Reset()
	capDir := t.TempDir()
	runsDir := filepath.Join(capDir, "state", "runs")
	require.NoError(t, os.MkdirAll(runsDir, 0o755))
	for i := 0; i <= verifier.AuditScanCap; i++ {
		name := "entry-" + strings.Repeat("0", 5-len(strconv.Itoa(i))) + strconv.Itoa(i) + ".json"
		require.NoError(t, os.WriteFile(filepath.Join(runsDir, name), []byte("{}"), 0o644))
	}
	code = verifier.RunAuditReplayCLI([]string{"--state-dir", capDir, "--run", "any"}, io)
	assert.Equal(t, verifier.AuditCLIExitRuntime, code)
	assert.Contains(t, errOut.String(), "replay failed")
}

func TestCOV_VERIFIER_ComputePatchRefBadDocPath(t *testing.T) {
	_, err := verifier.ComputePatchRef(&schemas.DocPatch{DocPath: "../escape.md"})
	require.Error(t, err)
}

func TestCOV_VERIFIER_CrossPatchTrimsUnrelatedConflicts(t *testing.T) {
	report := crossPatchCleanReport(t, crossPaymentsDocPath)
	patches := []*schemas.DocPatch{claimedPaymentsPatch(t), conflictingSiblingPatch(t)}

	// A conflict list with a DUPLICATED claim id: the duplicate folds once
	// (first-seen dedupe), never twice.
	after, err := verifier.ApplyCrossPatchConflict(report, []patch.ClaimValueConflict{
		{ClaimID: claimIdempotency, Conflicts: [][]patch.ClaimValueRecord{
			{{DocPath: crossPaymentsDocPath, Value: "x"}},
			{{DocPath: crossArchitectureDocPath, Value: "y"}},
		}},
		{ClaimID: claimIdempotency, Conflicts: [][]patch.ClaimValueRecord{
			{{DocPath: crossPaymentsDocPath, Value: "y"}},
			{{DocPath: crossArchitectureDocPath, Value: "x"}},
		}},
	}, 0, patches)
	require.NoError(t, err)
	require.NotNil(t, after)
	require.NotNil(t, after.CrossPatchConflict)
	recordCount := 0
	for _, check := range after.Tiers[1].Checks {
		if check.ID == "cross-patch-consistency" {
			recordCount++
		}
	}
	assert.Equal(t, 1, recordCount)

	// Appending again is idempotent: still ONE record (detail dedupe).
	again, err := verifier.ApplyCrossPatchConflict(after, []patch.ClaimValueConflict{
		{ClaimID: claimIdempotency, Conflicts: [][]patch.ClaimValueRecord{
			{{DocPath: crossPaymentsDocPath, Value: "x"}},
			{{DocPath: crossArchitectureDocPath, Value: "y"}},
		}},
	}, 0, patches)
	require.NoError(t, err)
	recordCount = 0
	for _, check := range again.Tiers[1].Checks {
		if check.ID == "cross-patch-consistency" {
			recordCount++
		}
	}
	assert.Equal(t, 1, recordCount)

	// Out-of-range patch index returns the input UNCHANGED (same reference).
	same, err := verifier.ApplyCrossPatchConflict(report, nil, 9, patches)
	require.NoError(t, err)
	assert.Same(t, report, same)
}

func TestCOV_VERIFIER_HeadVerificationBranchMatrix(t *testing.T) {
	sourceContent := "line one\ncalls refundPayment() here\ndefaults to `0.94` there\n"
	line := 2
	bind := func(index int) verifier.ClaimBinding {
		return verifier.ClaimBinding{Index: index, Bound: true, File: "src/x.ts", Line: &line}
	}
	decomposed := []verifier.DecomposedClaim{
		{Index: 0, Text: "observational note", Kind: verifier.DecomposedClaimKindOther},
		{Index: 1, Text: "callers observe idempotent behavior", Kind: verifier.DecomposedClaimKindBehavioral},
		{Index: 2, Text: "callers observe concurrent behavior", Kind: verifier.DecomposedClaimKindBehavioral},
		{Index: 3, Text: "calls `refundPayment()` here", Kind: verifier.DecomposedClaimKindSignature},
		{Index: 4, Text: "defaults to `0.94`", Kind: verifier.DecomposedClaimKindValue},
		{Index: 5, Text: "some other value `9.9`", Kind: verifier.DecomposedClaimKindValue},
		{Index: 6, Text: "tokenless claim", Kind: verifier.DecomposedClaimKindOther},
	}
	bindings := []verifier.ClaimBinding{
		bind(0), bind(1), bind(2), bind(3), bind(4), bind(5), bind(6),
		{Index: 7, Bound: true, File: "missing.ts", Line: &line},
	}
	// claim 7 is checked via an extra decomposed entry for the missing file.
	decomposed = append(decomposed, verifier.DecomposedClaim{Index: 7, Text: "calls `absentFunc()`", Kind: verifier.DecomposedClaimKindSignature})

	tracker, err := verifier.CreateClaimStatusTracker(decomposed)
	require.NoError(t, err)
	deps := &verifier.HeadVerifyDeps{
		ContractCorroborated: func(claim verifier.DecomposedClaim) bool {
			return claim.Index == 1 // behavioral corroborated only for index 1
		},
	}
	input := verifier.Tier2Input{
		DocContent:       t1StrPtr(sourceContent),
		PatchedContent:   t1StrPtr(sourceContent),
		WorkspaceSources: map[string]string{"src/x.ts": sourceContent},
		SourceHeadShas:   map[string]string{"src/x.ts": strings.Repeat("a", 40)},
	}
	check, err := verifier.CheckHeadVerification(input, decomposed, bindings, tracker, deps)
	require.NoError(t, err)
	require.NotNil(t, check.Detail)
	snapshot := tracker.Snapshot()
	assert.Equal(t, schemas.CheckFail, check.Status) // index 5 mismatches
	assert.Equal(t, schemas.ClaimVerdictUnchecked, snapshot[0].Status)
	assert.Equal(t, schemas.ClaimVerdictSupported, snapshot[1].Status)
	assert.Equal(t, schemas.ClaimVerdictBehavioralUnverified, snapshot[2].Status)
	assert.Equal(t, schemas.ClaimVerdictSupported, snapshot[3].Status)
	assert.Equal(t, schemas.ClaimVerdictSupported, snapshot[4].Status)
	assert.Equal(t, schemas.ClaimVerdictMismatched, snapshot[5].Status)
	assert.Equal(t, schemas.ClaimVerdictUnchecked, snapshot[6].Status)
	assert.Equal(t, schemas.ClaimVerdictUnchecked, snapshot[7].Status)
}
