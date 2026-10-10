package gitpr_test

// Black-box port of the partial-pass surface asserted by
// test/gitpr-flow.test.ts, test/gitpr-flow-p12-t17.test.ts and
// test/write-path-invariants-p12-t17.property.test.ts (P6-T10; PLAN §7.3 line
// 1143; D4; ARCH §12.2): a single failing document's verification must NOT
// block the others' merge-worthiness — the failing doc is DROPPED from the PR
// and REPORTED, never silently.

import (
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

func partialPassReport(verdict schemas.VerificationVerdict, patchRef string) *schemas.VerificationReport {
	return &schemas.VerificationReport{
		Schema:        "docsync.verification-report/v1",
		RunID:         "run_01J",
		DocPath:       "docs/payments.md",
		PatchRef:      patchRef,
		Verdict:       verdict,
		AutoEligible:  verdict == schemas.VerdictPass,
		PolicyVersion: 17,
	}
}

func TestP6T10_PartialPassAssemblesIncludedAndDropped(t *testing.T) {
	result, err := gitpr.AssemblePartialPass([]gitpr.PartialPassCandidate{
		{DocPath: "docs/payments.md", Verified: true, Verification: partialPassReport(schemas.VerdictPass, "patch-payments")},
		{DocPath: "docs/api.md", Verified: false, Verification: partialPassReport(schemas.VerdictReject, "patch-api")},
		{DocPath: "docs/cli.md", Verified: true},
		{DocPath: "docs/legacy.md", Verified: false, Detail: "  baseline missing  "},
	})
	require.NoError(t, err)

	// Input order is preserved for BOTH lists (determinism, ARCH §13).
	require.Len(t, result.Included, 2)
	require.Equal(t, "docs/payments.md", result.Included[0].DocPath)
	require.NotNil(t, result.Included[0].PatchRef)
	require.Equal(t, "patch-payments", *result.Included[0].PatchRef)
	require.Equal(t, "docs/cli.md", result.Included[1].DocPath)
	require.Nil(t, result.Included[1].PatchRef, "a verified doc without a report carries NO invented patchRef")

	require.Len(t, result.Dropped, 2)
	require.Equal(t, "docs/api.md", result.Dropped[0].DocPath)
	require.Equal(t, gitpr.PartialPassDropReason, result.Dropped[0].Reason)
	require.Contains(t, result.Dropped[0].Detail, "verification verdict 'reject'")
	require.Contains(t, result.Dropped[0].Detail, "reported, never silently")

	require.Equal(t, "docs/legacy.md", result.Dropped[1].DocPath)
	require.Equal(t, "baseline missing", result.Dropped[1].Detail, "caller detail is carried verbatim (trimmed)")
}

func TestP6T10_PartialPassEmptyListsRenderNone(t *testing.T) {
	result, err := gitpr.AssemblePartialPass(nil)
	require.NoError(t, err)
	rendered := gitpr.RenderPartialPassReport(result)
	require.Equal(t, strings.Join([]string{
		"## Partial pass (D4)",
		"",
		"Included documents (verification passed — merge-worthy):",
		"",
		"None.",
		"",
		"Dropped documents (verification not passed — reported, never silently):",
		"",
		"None.",
		"",
	}, "\n"), rendered)
}

func TestP6T10_PartialPassReportIsByteStable(t *testing.T) {
	result, err := gitpr.AssemblePartialPass([]gitpr.PartialPassCandidate{
		{DocPath: "docs/payments.md", Verified: true, Verification: partialPassReport(schemas.VerdictPass, "patch-payments")},
		{DocPath: "docs/api.md", Verified: false, Detail: "anchors | unresolved\nacross lines"},
		{DocPath: "docs/never-verified.md", Verified: false},
	})
	require.NoError(t, err)

	rendered := gitpr.RenderPartialPassReport(result)
	require.Equal(t, strings.Join([]string{
		"## Partial pass (D4)",
		"",
		"Included documents (verification passed — merge-worthy):",
		"",
		"| Document | Patch ref |",
		"| --- | --- |",
		"| `docs/payments.md` | `patch-payments` |",
		"",
		"Dropped documents (verification not passed — reported, never silently):",
		"",
		"| Document | Reason | Detail |",
		"| --- | --- | --- |",
		"| `docs/api.md` | verification_not_passed | anchors \\| unresolved across lines |",
		"| `docs/never-verified.md` | verification_not_passed | no verification evidence — dropped from the PR (partial pass, D4: reported, never silently) |",
		"",
	}, "\n"), rendered)

	// Same input → byte-identical output (P19, no clocks/IO).
	require.Equal(t, rendered, gitpr.RenderPartialPassReport(result))
	require.True(t, strings.HasSuffix(rendered, "\n"))
	require.False(t, strings.HasSuffix(rendered, "\n\n"))
}

func TestP6T10_PartialPassInputIsFailClosed(t *testing.T) {
	_, err := gitpr.AssemblePartialPass([]gitpr.PartialPassCandidate{{Verified: true}})
	require.ErrorContains(t, err, "candidates[0].docPath must be a nonempty string")

	_, err = gitpr.AssemblePartialPass([]gitpr.PartialPassCandidate{
		{DocPath: "docs/ok.md", Verified: true},
		{Verified: true},
	})
	require.ErrorContains(t, err, "candidates[1].docPath must be a nonempty string")
}
