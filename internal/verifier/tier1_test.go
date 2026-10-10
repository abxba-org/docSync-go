package verifier_test

// Black-box port of the verifier TS tier-1 suites (P4-T02..T09; PLAN §5.4
// items 2–9; ARCH §4.8 Tier 1; invariants 5/12/13 posture at verification):
// test/tier1-checks.test.ts + test/tier1-gates.test.ts.
//
//   - V-P4-1 (partial, tier 1 only): the golden valid patch passes ALL tier-1
//     checks;
//   - V-P4-2: an oversized patch (measured churn far beyond the triggering
//     diff) is REJECTED at tier 1 with the recorded bound measurement;
//   - V-P4-3: a whole-document rewrite is rejected OUTRIGHT regardless of
//     content quality — two failure records (apply abstention + the structural
//     rule) and no fabricated downstream simulation;
//   - V-P4-4: patches targeting locked/generated/external docs fail the D2
//     ownership-floor re-check at verification (defense in depth), and
//     doc_paths outside the manifest allowlist fail the allowlist check;
//   - V-P4-5: a patch breaking a Markdown fence (unclosed opener, yaml fence
//     break, json fence break) or deleting a heading fails structural-integrity
//     with line-precise evidence.
//
// All fixtures are content-injected (no real git unless stated); the clock is
// injected (t1InjectedClock); no network anywhere. The TS V-P4-1 suite reads head
// content from a real git repo created by @docsync/core/testing — the Go tree
// has no such fixture module yet, so head content is served through a recorded
// catFile double (AGENTS.md: recorded doubles).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
)

// t1DocModeExternal is the `external` pseudo-mode of the TS WorkspaceMode
// union (DocMode | 'external' — core/src/workspace/types.ts
// WORKSPACE_MODE_VALUES). The manifest mode union has no 'external' member, so
// it renders through schemas.DocMode exactly like
// verifier.docModeExternal does on the library side (verification_stage.go).
const t1DocModeExternal schemas.DocMode = "external"

// t1CatFileDouble is a recorded catFile double standing in for the real
// payments-fixture git repo (the engine only reads it when no worktree content
// was injected; the double serves the fixture's head content).
type t1CatFileDouble struct {
	content string
	calls   []string
}

func (c *t1CatFileDouble) CatFile(_ context.Context, rev, path string) (gitx.CatFileResult, error) {
	c.calls = append(c.calls, rev+" "+path)
	return gitx.CatFileResult{BlobSha: strings.Repeat("a", 40), Content: c.content}, nil
}

/* ------------------------------------------- V-P4-1: valid patch passes tier 1 */

func TestVP4_1_GoldenPatchPassesAllEightChecksOverThePaymentsFixture(t *testing.T) {
	// The fixture's claim line is pinned at line 8 (0-based index 7).
	require.Equal(t, t1PaymentsClaimLine, strings.Split(t1PaymentsFixtureDoc, "\n")[7])

	catFile := &t1CatFileDouble{content: t1PaymentsFixtureDoc}
	patchObject := t1ClaimFixPatch(t1PaymentsFixtureDoc)
	input := t1Input(patchObject, t1StrPtr(t1PaymentsFixtureDoc))
	input.ManifestAllowlist = map[string]bool{"docs/payments.md": true}
	input.Git = catFile
	input.Rev = t1StrPtr("HEAD")

	result, err := verifier.RunTier1(input)
	require.NoError(t, err)

	assert.Equal(t, schemas.Tier1, result.Tier)
	assert.Equal(t, schemas.CheckPass, result.Status)
	assert.Equal(t, t1CheckOrder, t1CheckIDs(result))
	for _, check := range result.Checks {
		assert.Equal(t, schemas.CheckPass, check.Status, "check %s", check.ID)
	}
	require.NotNil(t, result.Context.ApplyOutcome)
	assert.Equal(t, patch.OutcomeApplied, result.Context.ApplyOutcome.Kind)
	require.NotNil(t, result.Context.PatchedContent)
	assert.Equal(t,
		strings.Replace(t1PaymentsFixtureDoc, t1PaymentsClaimLine, t1FixedClaimLine, 1),
		*result.Context.PatchedContent)
}

func TestVP4_1_TheSameGoldenPatchPassesFromInjectedContentAlone(t *testing.T) {
	patchObject := t1ClaimFixPatch(t1ProseDoc)
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	require.NotNil(t, result.Context.PatchedContent)
	assert.Contains(t, *result.Context.PatchedContent, t1FixedClaimLine)
	for _, check := range result.Checks {
		assert.Equal(t, schemas.CheckPass, check.Status, "check %s", check.ID)
	}
}

/* ------------------------------------- V-P4-2: oversized patch rejected at tier 1 */

func TestVP4_2_OversizedPatchFailsMinimalityBoundWithRecordedMeasurement(t *testing.T) {
	// 80 non-empty lines; replaces ONE body line with 45 unique prose lines —
	// below the whole-document-rewrite ratio (46/80 < 0.9) so the application
	// succeeds and the measurement is real, but far above the bound (46 > 20).
	bodyLines := make([]string, 0, 79)
	for index := 1; index <= 79; index++ {
		bodyLines = append(bodyLines, fmt.Sprintf("Body line %d.", index))
	}
	bigDoc := "# Payments\n" + strings.Join(bodyLines, "\n") + "\n"
	replacementLines := make([]string, 0, 45)
	for index := 1; index <= 45; index++ {
		replacementLines = append(replacementLines, fmt.Sprintf("Correction line %d.", index))
	}
	replacements := strings.Join(replacementLines, "\n")
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: bigDoc,
		hunks:       t1LineEdits(bigDoc, []t1LineEdit{{line: "Body line 40.", replacement: replacements}}),
	})

	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(bigDoc), t1Override{
		triggeringDiffStats: &patch.DiffStats{Added: 1, Removed: 0},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	bound := t1CheckOf(t, result, "minimality-bound")
	assert.Equal(t, schemas.CheckFail, bound.Status)
	t1AssertDetailContains(t, bound, "added 45 / removed 1")
	t1AssertDetailContains(t, bound, "46") // measured ratio
	t1AssertDetailContains(t, bound, "20") // the bound (patch.DefaultMinimalityMaxRatio)

	require.NotNil(t, result.Context.Minimality)
	assert.Equal(t, schemas.DocPatchMinimality{Measured: true, AddedLines: 45, RemovedLines: 1},
		result.Context.Minimality.Minimality)
	assert.True(t, result.Context.Minimality.Oversized)
	require.NotNil(t, result.Context.Minimality.Ratio)
	assert.InDelta(t, 46.0, *result.Context.Minimality.Ratio, 1e-12)

	// The tier still emits ALL EIGHT records; only the bound check fails.
	assert.Equal(t, t1CheckOrder, t1CheckIDs(result))
	assert.Equal(t, []string{"minimality-bound"}, t1FailedIDs(result))
}

func TestVP4_2_PerInputBoundOverrideIsHonored(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1ProseDoc,
		hunks: t1LineEdits(t1ProseDoc, []t1LineEdit{{
			line:        t1PaymentsClaimLine,
			replacement: t1FixedClaimLine + "\nExtra context line A.\nExtra context line B.",
		}}),
	})
	// added 3 / removed 1 against trigger {added: 1, removed: 0} ⇒ ratio 4 > 3.
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc), t1Override{
		triggeringDiffStats: &patch.DiffStats{Added: 1, Removed: 0},
		minRatio:            t1Float64p(3),
	}))
	require.NoError(t, err)

	bound := t1CheckOf(t, result, "minimality-bound")
	assert.Equal(t, schemas.CheckFail, bound.Status)
	t1AssertDetailContains(t, bound, "exceeds the minimality bound 3")
	assert.Equal(t, schemas.CheckFail, result.Status)
}

/* ------------------------- V-P4-3: whole-document rewrite rejected outright */

func TestVP4_3_BodyReplacementTripsTheStructuralRule_TwoFailureRecords(t *testing.T) {
	body := strings.Join(t1RewriteDocLines[1:], "\n") // 11 of 12 non-empty lines ⇒ >= 0.9
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1RewriteDoc,
		hunks: []schemas.PatchHunk{
			t1SpanReplace(t1RewriteDoc, body, "Fresh body — the payments policy, rewritten in full."),
		},
	})

	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1RewriteDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.Tier1, result.Tier)
	assert.Equal(t, schemas.CheckFail, result.Status)

	// ALL EIGHT records in canonical order — a rewrite never silently skips.
	assert.Equal(t, t1CheckOrder, t1CheckIDs(result))
	assert.Equal(t,
		[]string{"patch-applies-cleanly", "whole-document-rewrite"}, t1FailedIDs(result))

	t1AssertDetailContains(t, t1CheckOf(t, result, "patch-applies-cleanly"), "rewrite-rejected")
	rewrite := t1CheckOf(t, result, "whole-document-rewrite")
	assert.Equal(t, schemas.CheckFail, rewrite.Status)
	t1AssertDetailContains(t, rewrite, "deletion_span")
	t1AssertDetailContains(t, rewrite, "invariant 12")
	require.NotNil(t, rewrite.Detail)
	assert.Regexp(t, regexp.MustCompile(`measured ratio 0\.9\d+`), *rewrite.Detail)

	// No downstream simulation is fabricated for an abstained apply.
	for _, id := range []string{
		"minimality-bound", "structural-integrity", "link-resolution", "examples-parse",
	} {
		assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, id).Status, "check %s", id)
	}
	assert.Nil(t, result.Context.PatchedContent)
	require.NotNil(t, result.Context.ApplyOutcome)
	assert.Equal(t, patch.OutcomeAbstain, result.Context.ApplyOutcome.Kind)
}

func TestVP4_3_InsertDominatedRewriteTripsRuleB_StructuralDiff(t *testing.T) {
	smallDoc := "# Title\n\nOne.\nTwo.\nThree.\n" // 4 non-empty lines
	newLines := make([]string, 0, 40)
	for index := 1; index <= 40; index++ {
		newLines = append(newLines, fmt.Sprintf("New line %d.", index))
	}
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: smallDoc,
		hunks: []schemas.PatchHunk{
			t1SpanReplace(smallDoc, "One.", strings.Join(newLines, "\n")),
		},
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(smallDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	rewrite := t1CheckOf(t, result, "whole-document-rewrite")
	assert.Equal(t, schemas.CheckFail, rewrite.Status)
	t1AssertDetailContains(t, rewrite, "structural_diff")
	require.NotNil(t, rewrite.Detail)
	assert.Regexp(t, regexp.MustCompile(`measured ratio 1[0-9]\.`), *rewrite.Detail) // (40+1)/4 >= 10
}

/* ------------- V-P4-4: locked/generated/external rejected at verification (D2) */

func TestVP4_4_NonWritableModesFailTheOwnershipFloorReCheck(t *testing.T) {
	for _, tc := range []struct {
		mode schemas.DocMode
		path string
	}{
		{schemas.DocModeLocked, "docs/locked.md"},
		{schemas.DocModeGenerated, "docs/generated-api.md"},
		{t1DocModeExternal, "docs/external.md"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			patchObject := t1BuildPatch(t1BuildPatchOpts{
				docPath:     tc.path,
				baseContent: t1ProseDoc,
				hunks: t1LineEdits(t1ProseDoc, []t1LineEdit{
					{line: t1PaymentsClaimLine, replacement: t1FixedClaimLine},
				}),
			})
			// Adversarial misconfiguration: the application handed a
			// writable-path set that includes the non-writable doc — the
			// verifier still refuses.
			result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc), t1Override{
				targetMode:        t1ModePtr(tc.mode),
				manifestAllowlist: map[string]bool{tc.path: true},
			}))
			require.NoError(t, err)

			assert.Equal(t, schemas.CheckFail, result.Status)
			ownership := t1CheckOf(t, result, "ownership-floor")
			assert.Equal(t, schemas.CheckFail, ownership.Status)
			t1AssertDetailContains(t, ownership, "'"+string(tc.mode)+"'")
			t1AssertDetailContains(t, ownership, "D2 ownership floor re-check")
			assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "manifest-allowlist-only").Status)
			// The simulation itself ran (the adversarial allowlist admitted it).
			assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "patch-applies-cleanly").Status)
		})
	}
}

func TestVP4_4_LockedTargetWithTheRealisticAllowlistFailsAllThree(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		docPath:     "docs/locked.md",
		baseContent: t1ProseDoc,
		hunks: t1LineEdits(t1ProseDoc, []t1LineEdit{
			{line: t1PaymentsClaimLine, replacement: t1FixedClaimLine},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc), t1Override{
		targetMode:        t1ModePtr(schemas.DocModeLocked),
		manifestAllowlist: map[string]bool{"docs/payments.md": true},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "patch-applies-cleanly"), "guard-refused")
	assert.Equal(t, schemas.CheckFail, t1CheckOf(t, result, "manifest-allowlist-only").Status)
	assert.Equal(t, schemas.CheckFail, t1CheckOf(t, result, "ownership-floor").Status)
	for _, id := range []string{
		"minimality-bound", "structural-integrity", "link-resolution", "examples-parse",
	} {
		assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, id).Status, "check %s", id)
	}
}

func TestVP4_4_DocPathOutsideTheAllowlistFailsManifestAllowlistOnly(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		docPath:     "docs/unlisted.md",
		baseContent: t1ProseDoc,
		hunks: t1LineEdits(t1ProseDoc, []t1LineEdit{
			{line: t1PaymentsClaimLine, replacement: t1FixedClaimLine},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	allowlist := t1CheckOf(t, result, "manifest-allowlist-only")
	assert.Equal(t, schemas.CheckFail, allowlist.Status)
	t1AssertDetailContains(t, allowlist, "'docs/unlisted.md'")
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "ownership-floor").Status) // mode is writable — the PATH is the defect
	t1AssertDetailContains(t, t1CheckOf(t, result, "patch-applies-cleanly"), "guard-refused")
}

/* ------------------- V-P4-5: fence breaks rejected with structural evidence */

func TestVP4_5_UnclosedFenceOpenerFailsStructuralIntegrity(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1FenceDoc,
		hunks: t1LineEdits(t1FenceDoc, []t1LineEdit{
			{line: "Escalation contacts live in docs/oncall.md.", replacement: "```"},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1FenceDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	structural := t1CheckOf(t, result, "structural-integrity")
	assert.Equal(t, schemas.CheckFail, structural.Status)
	t1AssertDetailContains(t, structural, "unclosed code fence opened at line 18")
	// Only the structural check fails (the unclosed block declares no language).
	assert.Equal(t, []string{"structural-integrity"}, t1FailedIDs(result))
}

func TestVP4_5_YamlFenceBodyBreakFailsStructuralIntegrityAndExamplesParse(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1FenceDoc,
		hunks: t1LineEdits(t1FenceDoc, []t1LineEdit{
			{line: "  window: 24h", replacement: "window: [unclosed"},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1FenceDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	structural := t1CheckOf(t, result, "structural-integrity")
	assert.Equal(t, schemas.CheckFail, structural.Status)
	t1AssertDetailContains(t, structural, "code fence tagged yaml at line 7 is not valid YAML")
	assert.Equal(t, schemas.CheckFail, t1CheckOf(t, result, "examples-parse").Status)
}

func TestVP4_5_JsonFenceBodyBreakFailsStructuralIntegrityAndExamplesParse(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1FenceDoc,
		hunks: t1LineEdits(t1FenceDoc, []t1LineEdit{
			{line: `{"amount": 1000}`, replacement: "{ not json"},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1FenceDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	structural := t1CheckOf(t, result, "structural-integrity")
	assert.Equal(t, schemas.CheckFail, structural.Status)
	t1AssertDetailContains(t, structural, "code fence tagged json at line 14 is not valid JSON")
	assert.Equal(t, schemas.CheckFail, t1CheckOf(t, result, "examples-parse").Status)
}

func TestVP4_5_HeadingDeletedByAHunkIsSilentStructuralMangling(t *testing.T) {
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1FenceDoc,
		hunks: t1LineEdits(t1FenceDoc, []t1LineEdit{
			{line: "## Configuration", replacement: "Configuration details follow."},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1FenceDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	structural := t1CheckOf(t, result, "structural-integrity")
	assert.Equal(t, schemas.CheckFail, structural.Status)
	t1AssertDetailContains(t, structural, "headings disappeared from the patched content")
	t1AssertDetailContains(t, structural, "## Configuration")
	t1AssertDetailContains(t, structural, "(pre 1, post 0)")
	assert.Equal(t, []string{"structural-integrity"}, t1FailedIDs(result))
}

/* -------------------------------------------- link-resolution (tier1-checks) */

func TestLinkResolution_ResolvableAnchorTargetAndAllowlistedExternalPass(t *testing.T) {
	doc := t1LinksDoc(
		"See [the refund flow](#refunds), [the guide](../guide.md#setup), [the sibling](./refunds.md), and [the spec](https://example.com/spec).",
	)
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "link-resolution").Status)
}

func TestLinkResolution_InternalAnchorMatchingNoHeadingFails(t *testing.T) {
	doc := t1LinksDoc("See [broken](#does-not-exist).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	links := t1CheckOf(t, result, "link-resolution")
	assert.Equal(t, schemas.CheckFail, links.Status)
	t1AssertDetailContains(t, links, "'#does-not-exist' does not match any heading")
}

func TestLinkResolution_UnknownDocAndMissingFragmentFail(t *testing.T) {
	unknown := t1LinksDoc("See [nowhere](./nowhere.md).")
	resultUnknown, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(unknown), t1StrPtr(unknown), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, resultUnknown.Status)
	t1AssertDetailContains(t, t1CheckOf(t, resultUnknown, "link-resolution"),
		"'docs/nowhere.md' which is not a known doc")

	fragmentMiss := t1LinksDoc("See [the guide](../guide.md#missing).")
	resultFragment, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(fragmentMiss), t1StrPtr(fragmentMiss), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, resultFragment.Status)
	t1AssertDetailContains(t, t1CheckOf(t, resultFragment, "link-resolution"),
		"'#missing' in 'guide.md' does not match any heading of the target document")
}

func TestLinkResolution_RelativeLinkWithoutAKnownDocsIndexFailsClosed(t *testing.T) {
	doc := t1LinksDoc("See [the guide](../guide.md).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc))) // no t1LinkInputs at all
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "link-resolution"), "known-docs index has 0 path(s)")
}

func TestLinkResolution_ExternalURLNotAllowlistedWithoutAPortFailsClosed(t *testing.T) {
	doc := t1LinksDoc("See [the spec](https://example.com/spec).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &verifier.LinkCheckInputs{KnownDocs: map[string]string{}},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	links := t1CheckOf(t, result, "link-resolution")
	assert.Equal(t, schemas.CheckFail, links.Status)
	t1AssertDetailContains(t, links,
		"external URL not allowlisted and no reachability port configured")
}

func TestLinkResolution_ReachabilityPortDecidesUnallowlistedExternalURLs(t *testing.T) {
	doc := t1LinksDoc("See [the spec](https://example.com/spec).")

	accepted, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &verifier.LinkCheckInputs{
			KnownDocs:   map[string]string{},
			IsReachable: func(string) (bool, error) { return true, nil },
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckPass, accepted.Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, accepted, "link-resolution").Status)

	rejected, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &verifier.LinkCheckInputs{
			KnownDocs:   map[string]string{},
			IsReachable: func(url string) (bool, error) { return url != t1SpecURL, nil },
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, rejected.Status)
	t1AssertDetailContains(t, t1CheckOf(t, rejected, "link-resolution"),
		"rejected by the reachability port")
}

func TestLinkResolution_LinksInsideFencedBlocksAreLiteralContent(t *testing.T) {
	docText := strings.Join([]string{
		"# Payments",
		"",
		t1PaymentsClaimLine,
		"",
		"```",
		"[fake](#missing-anchor) and https://example.com/inside-fence",
		"```",
	}, "\n") + "\n"
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(docText), t1StrPtr(docText), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "link-resolution").Status)
}

/* ------------------------------ structural-integrity extras (tier1-checks) */

// t1FrontMatterDoc mirrors the TS FRONT_MATTER_DOC fixture.
var t1FrontMatterDoc = strings.Join([]string{
	"---",
	"title: Payments",
	"mode: reference",
	"---",
	"",
	"# Payments",
	"",
	t1PaymentsClaimLine,
}, "\n") + "\n"

func TestStructuralIntegrity_FrontMatterParsesOrFailsWithTheParseError(t *testing.T) {
	ok, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1FrontMatterDoc), t1StrPtr(t1FrontMatterDoc)))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckPass, ok.Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, ok, "structural-integrity").Status)

	broken := strings.Replace(t1FrontMatterDoc, "mode: reference", "mode: [unclosed", 1)
	// The patch anchors on the BROKEN snapshot itself (base hash matches) so
	// the application succeeds and the verifier judges the patched result.
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(broken), t1StrPtr(broken)))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckFail, result.Status)
	structural := t1CheckOf(t, result, "structural-integrity")
	assert.Equal(t, schemas.CheckFail, structural.Status)
	t1AssertDetailContains(t, structural, "front-matter is not valid YAML")
}

func TestStructuralIntegrity_HeadingLookalikeInsideADeletedFenceWasNeverAHeading(t *testing.T) {
	doc := strings.Join([]string{
		"# Payments",
		"",
		t1PaymentsClaimLine,
		"",
		"```yaml",
		"# fake heading inside a fence",
		"window: 24h",
		"```",
		"",
		"More prose follows here.",
		"And one more line of prose.",
	}, "\n") + "\n"
	// The patch deletes the WHOLE fence (heading-lookalike included): if the
	// scanner miscounted fenced lines as headings, pre/post multisets would
	// disagree and structural-integrity would fail — it must pass.
	deleteText := strings.Join([]string{
		"```yaml",
		"# fake heading inside a fence",
		"window: 24h",
		"```",
	}, "\n")
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		baseContent: doc,
		hunks:       []schemas.PatchHunk{t1SpanReplace(doc, deleteText, "The fence was removed on purpose.")},
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(doc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "structural-integrity").Status)
}

/* --------------------------------------------------- examples-parse (P4-T07) */

func TestExamplesParse_ValidJsonAndYamlBlocksParse_OthersAreRecordedSkips(t *testing.T) {
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ExamplesDoc), t1StrPtr(t1ExamplesDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	examples := t1CheckOf(t, result, "examples-parse")
	assert.Equal(t, schemas.CheckPass, examples.Status)
	require.NotNil(t, examples.Detail)
	assert.Equal(t, "2 block(s) parsed, 1 skipped (no tooling declared)", *examples.Detail)
}

func TestExamplesParse_MalformedJsonAndYamlBlocksFailWithLinePreciseDetail(t *testing.T) {
	patchJson := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1ExamplesDoc,
		hunks: t1LineEdits(t1ExamplesDoc, []t1LineEdit{
			{line: `{"amount": 1000}`, replacement: `{"amount": }`},
		}),
	})
	resultJson, err := verifier.RunTier1(t1Input(patchJson, t1StrPtr(t1ExamplesDoc)))
	require.NoError(t, err)
	t1AssertDetailContains(t, t1CheckOf(t, resultJson, "examples-parse"), "json block at line 5 does not parse")

	patchYaml := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1ExamplesDoc,
		hunks: t1LineEdits(t1ExamplesDoc, []t1LineEdit{
			{line: "window: 24h", replacement: "window: [unclosed"},
		}),
	})
	resultYaml, err := verifier.RunTier1(t1Input(patchYaml, t1StrPtr(t1ExamplesDoc)))
	require.NoError(t, err)
	t1AssertDetailContains(t, t1CheckOf(t, resultYaml, "examples-parse"), "yaml block at line 9 does not parse")
}

func TestExamplesParse_ExampleCheckerPortIsInvokedPerLanguageTaggedBlock(t *testing.T) {
	var seen []string
	checker := func(block verifier.ExampleBlock) (verifier.ExampleCheckResult, error) {
		seen = append(seen, block.Language)
		if block.Language == "ts" {
			return verifier.ExampleCheckResult{Ok: false, Detail: t1StrPtr("no ts parser in v1")}, nil
		}
		return verifier.ExampleCheckResult{Ok: true}, nil
	}
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ExamplesDoc), t1StrPtr(t1ExamplesDoc), t1Override{
		exampleChecker: checker,
	}))
	require.NoError(t, err)

	assert.Equal(t, []string{"json", "yaml", "ts"}, seen)
	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "examples-parse"),
		"block tagged 'ts' at line 13 rejected by the example checker: no ts parser in v1")

	okResult, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ExamplesDoc), t1StrPtr(t1ExamplesDoc), t1Override{
		exampleChecker: func(verifier.ExampleBlock) (verifier.ExampleCheckResult, error) {
			return verifier.ExampleCheckResult{Ok: true}, nil
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, schemas.CheckPass, okResult.Status)
	examples := t1CheckOf(t, okResult, "examples-parse")
	require.NotNil(t, examples.Detail)
	assert.Equal(t, "3 block(s) parsed, 0 skipped (no tooling declared)", *examples.Detail)
}

/* --------------------------------- application outcome propagation (P4-T02) */

func TestApplyOutcomes_StaleBaseHashWithoutASnapshotAbstainsTheRace(t *testing.T) {
	racedDoc := strings.Replace(t1ProseDoc,
		t1PaymentsClaimLine, "Refund requests require an `idempotencyKey` (updated).", 1)
	patchObject := t1ClaimFixPatch(t1ProseDoc)
	// doc changed since the writer read it
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(racedDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckFail, apply.Status)
	t1AssertDetailContains(t, apply, "doc-changed-race-unverified")
	for _, id := range []string{
		"minimality-bound", "structural-integrity", "link-resolution", "examples-parse",
	} {
		assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, id).Status, "check %s", id)
	}
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "whole-document-rewrite").Status) // pure predicate still runs
}

func TestApplyOutcomes_UnanchoredPatchFailsWithTheHunkIndexInTheDetail(t *testing.T) {
	patchObject := t1ClaimFixPatch(t1ProseDoc)
	// Corrupt the anchor hash AFTER validation: a writer misquote.
	stale := *patchObject
	staleHunks := make([]schemas.PatchHunk, len(patchObject.Hunks))
	copy(staleHunks, patchObject.Hunks)
	staleHunks[0].Anchor.ContentHash = strings.Repeat("a", 64)
	stale.Hunks = staleHunks

	result, err := verifier.RunTier1(t1Input(&stale, t1StrPtr(t1ProseDoc)))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckFail, apply.Status)
	t1AssertDetailContains(t, apply, "UNANCHORED")
	t1AssertDetailContains(t, apply, "hunk 0")
	require.NotNil(t, result.Context.ApplyOutcome)
	assert.Equal(t, patch.OutcomeUnanchored, result.Context.ApplyOutcome.Kind)
}

func TestApplyOutcomes_RunIdSkipEmitsASkippedApplyRecord_TierPasses(t *testing.T) {
	patchObject := t1ClaimFixPatch(t1ProseDoc)
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(t1ProseDoc), t1Override{
		isRunApplied: func(runID, docPath string) bool {
			return runID == patchObject.RunID && docPath == patchObject.DocPath
		},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckSkipped, apply.Status)
	t1AssertDetailContains(t, apply, "run-id skip")
	t1AssertDetailContains(t, apply, patchObject.RunID)
	assert.Nil(t, result.Context.PatchedContent)
	for _, id := range []string{
		"minimality-bound", "structural-integrity", "link-resolution", "examples-parse",
	} {
		assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, id).Status, "check %s", id)
	}
}

func TestApplyOutcomes_AlreadyAppliedPassesWithTheUnchangedDocument(t *testing.T) {
	patchObject := t1ClaimFixPatch(t1ProseDoc)
	alreadyPatched := strings.Replace(t1ProseDoc, t1PaymentsClaimLine, t1FixedClaimLine, 1)
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(alreadyPatched), t1Override{
		baseContent: t1StrPtr(t1ProseDoc), // race detected, writer snapshot supplied
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckPass, apply.Status)
	t1AssertDetailContains(t, apply, "already-applied")
	require.NotNil(t, result.Context.PatchedContent)
	assert.Equal(t, alreadyPatched, *result.Context.PatchedContent)
	require.NotNil(t, result.Context.Minimality)
	assert.Equal(t,
		schemas.DocPatchMinimality{Measured: true, AddedLines: 0, RemovedLines: 0},
		result.Context.Minimality.Minimality)
}

func TestApplyOutcomes_DocWithoutCurrentContentFailsClosed(t *testing.T) {
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ProseDoc), nil))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckFail, apply.Status)
	t1AssertDetailContains(t, apply, "document absent")
	assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, "whole-document-rewrite").Status)
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "manifest-allowlist-only").Status) // pure on input data — always runs
	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "ownership-floor").Status)
}

func TestApplyOutcomes_PureInsertionHunkAppendsAtTheEndOfDocumentGap(t *testing.T) {
	insertion := t1BuildPatch(t1BuildPatchOpts{
		baseContent: t1ProseDoc,
		hunks: []schemas.PatchHunk{
			t1InsertBefore(t1ProseDoc, 13, "Refunds flow through the pipeline documentation."),
		},
	})
	result, err := verifier.RunTier1(t1Input(insertion, t1StrPtr(t1ProseDoc), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, result.Status)
	require.NotNil(t, result.Context.PatchedContent)
	assert.Contains(t, *result.Context.PatchedContent, "Refunds flow through the pipeline documentation.")
}

/* --------------------------------------- run determinism + source purity */

func TestTier1Run_SameInputTwiceYieldsDeepEqualResults(t *testing.T) {
	input := t1Input(t1ClaimFixPatch(t1ProseDoc), t1StrPtr(t1ProseDoc), t1Override{t1LinkInputs: &t1LinkInputs})
	first, err := verifier.RunTier1(input)
	require.NoError(t, err)
	second, err := verifier.RunTier1(input)
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, t1CheckOrder, t1CheckIDs(second))
}

// TestTier1Purity_Tier1ModulesNeverTouchANetworkOrFileSystemSurface ports the
// TS no-network purity proof, scoped to this unit's tier1 modules (the TS
// proof walked src/ + src/tier1/; the concurrent tier2/tier3 units own their
// own files and their own proofs).
func TestTier1Purity_Tier1ModulesNeverTouchANetworkOrFileSystemSurface(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller")
	tier1Dir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(tier1Dir)
	require.NoError(t, err)

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "tier1_") && strings.HasSuffix(name, ".go") &&
			!strings.HasSuffix(name, "_test.go") {
			files = append(files, filepath.Join(tier1Dir, name))
		}
	}
	// The seven check sources + markdown + types (the eight check records:
	// apply, allowlist, ownership, minimality, rewrite-shared, structural,
	// links, examples).
	require.GreaterOrEqual(t, len(files), 9, "tier1 modules present")

	forbiddenNetwork := regexp.MustCompile(`(?i)^(net|net/http|os/exec|os|io/ioutil)(/.*)?$`)
	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		text := string(data)
		for _, match := range regexp.MustCompile(`"([a-z0-9_/.-]+)"`).FindAllStringSubmatch(text, -1) {
			if forbiddenNetwork.MatchString(match[1]) {
				t.Fatalf("%s imports a network/file-system surface %q", file, match[1])
			}
		}
		if strings.Contains(text, "fetch(") {
			t.Fatalf("%s calls fetch", file)
		}
	}
}

/* --------------------------- markdown scanner characterization (markdown.ts) */

// The TS markdown module has no dedicated test file of its own (its rules are
// covered indirectly through the checks); these characterizations pin the
// ported scanner/fence/heading/slug/link semantics the checks depend on.

func TestScanFences_ClosedOpenUnclosedAndInfoStringHandling(t *testing.T) {
	scan := verifier.ScanFences(strings.Join([]string{
		"prose",
		"```yaml",
		"a: 1",
		"```",
		"more",
		"   ```   extra info   ",
		"body",
		"``",
		"````",
	}, "\n"))
	require.Len(t, scan.Blocks, 2)
	assert.Nil(t, scan.UnclosedAt)

	first := scan.Blocks[0]
	assert.Equal(t, "yaml", first.Language)
	assert.Equal(t, "a: 1", first.Code)
	assert.Equal(t, 2, first.StartLine)
	assert.Equal(t, 4, first.EndLine)
	assert.False(t, first.Unclosed)

	// The info string's first word is the language, taken verbatim (the TS
	// implementation does not lowercase, whatever its doc comment says).
	second := scan.Blocks[1]
	assert.Equal(t, "extra", second.Language)
	assert.Equal(t, 6, second.StartLine)
	assert.Equal(t, 9, second.EndLine)
	assert.False(t, second.Unclosed)
	// Two backticks are content (a closer needs >= 3); four close a fence.
	assert.Equal(t, "body\n``", second.Code)

	// Fence-info and fence-content lines are fence-owned; "prose"/"more" are not.
	assert.True(t, scan.FencedLines[2])
	assert.True(t, scan.FencedLines[6])
	assert.True(t, scan.FencedLines[9])
	assert.False(t, scan.FencedLines[1])
	assert.False(t, scan.FencedLines[5])
}

func TestScanFences_UnclosedFenceRunsToEOFAndNestedBackticksAreContent(t *testing.T) {
	content := strings.Join([]string{
		"# Doc",
		"```json",
		"{\"a\": \"```\"}",
		"still inside",
	}, "\n")
	scan := verifier.ScanFences(content)
	require.Len(t, scan.Blocks, 1)
	require.NotNil(t, scan.UnclosedAt)
	assert.Equal(t, 2, *scan.UnclosedAt)
	block := scan.Blocks[0]
	assert.True(t, block.Unclosed)
	assert.Equal(t, len(strings.Split(content, "\n")), block.EndLine)
	assert.Equal(t, "{\"a\": \"```\"}\nstill inside", block.Code)
}

func TestScanFences_FourLeadingSpacesAreNotAFence(t *testing.T) {
	scan := verifier.ScanFences("    ```\nnot a fence\n    ```")
	assert.Empty(t, scan.Blocks)
	assert.Nil(t, scan.UnclosedAt)
}

func TestScanFrontMatter_OnlyALeadingTerminatorBlockIsFrontMatter(t *testing.T) {
	assert.Nil(t, verifier.ScanFrontMatter("no front matter\n"))

	fm := verifier.ScanFrontMatter("---\ntitle: x\n...\nbody\n")
	require.NotNil(t, fm)
	assert.Equal(t, "title: x", fm.YAML)
	assert.Equal(t, 3, fm.EndLine)
	assert.True(t, fm.Lines[1])
	assert.True(t, fm.Lines[2])
	assert.True(t, fm.Lines[3])
	assert.False(t, fm.Lines[4])

	// A `---` later in the document is not front-matter.
	assert.Nil(t, verifier.ScanFrontMatter("prose\n---\ntitle: x\n---\n"))
	// Unterminated front-matter is plain prose.
	assert.Nil(t, verifier.ScanFrontMatter("---\ntitle: x\n"))
}

func TestExtractHeadings_SkipsFencedLinesAndRejectsNonATX(t *testing.T) {
	content := strings.Join([]string{
		"# One",
		"####### seven",
		"#not-a-heading",
		"## Heading ##",
		"##",
		"   ### indented",
		"    # four spaces",
	}, "\n")
	headings := verifier.ExtractHeadings(content, nil)
	require.Len(t, headings, 3)
	assert.Equal(t, verifier.HeadingOccurrence{Level: 1, Text: "One", Line: 1}, headings[0])
	// The TS implementation keeps closing hashes in the captured text (its
	// doc comment says stripped; the code does not strip) — ported exactly.
	assert.Equal(t, verifier.HeadingOccurrence{Level: 2, Text: "Heading ##", Line: 4}, headings[1])
	assert.Equal(t, verifier.HeadingOccurrence{Level: 3, Text: "indented", Line: 6}, headings[2])

	skip := map[int]bool{1: true}
	filtered := verifier.ExtractHeadings(content, skip)
	require.Len(t, filtered, 2)
	assert.Equal(t, 4, filtered[0].Line)
}

func TestSlugifyHeading_GitHubSlugRules(t *testing.T) {
	assert.Equal(t, "hello-world", verifier.SlugifyHeading("Hello World"))
	assert.Equal(t, "hello-world", verifier.SlugifyHeading("Hello, World!"))
	assert.Equal(t, "snake_case-name", verifier.SlugifyHeading("snake_case-name"))
	assert.Equal(t, "ünïcode-héading", verifier.SlugifyHeading("Ünïcode Héading"))
	assert.Equal(t, "trimmed", verifier.SlugifyHeading("  --trimmed--  "))
	assert.Equal(t, "", verifier.SlugifyHeading("***"))
}

func TestHeadingAnchors_DuplicatesGetNumericSuffixesInDocumentOrder(t *testing.T) {
	content := strings.Join([]string{
		"# Payments",
		"## Setup",
		"## Setup",
		"### Setup",
	}, "\n")
	anchors := verifier.HeadingAnchors(content, nil)
	assert.Equal(t, map[string]bool{
		"payments": true,
		"setup":    true,
		"setup-1":  true,
		"setup-2":  true,
	}, anchors)
}

func TestExtractLinks_InlineLinksImagesAndTitles(t *testing.T) {
	line := "See [one](a.md) and ![img](b.png) plus [t](c.md \"Title\") plus [ref][x] plus <https://auto>"
	links := verifier.ExtractLinks(line)
	require.Len(t, links, 3)
	assert.Equal(t, verifier.LinkOccurrence{Target: "a.md", Line: 1}, links[0])
	assert.Equal(t, verifier.LinkOccurrence{Target: "b.png", Line: 1}, links[1])
	assert.Equal(t, verifier.LinkOccurrence{Target: "c.md", Line: 1}, links[2])

	// Line numbers are 1-based and per line.
	multi := verifier.ExtractLinks("[x](1.md)\nprose\n[y](2.md)")
	require.Len(t, multi, 2)
	assert.Equal(t, 1, multi[0].Line)
	assert.Equal(t, 3, multi[1].Line)
}

func TestParseYAMLStrict_WrapsTheParseErrorInATypedDefect(t *testing.T) {
	require.NoError(t, verifier.ParseYAMLStrict("a: 1\n", "front-matter"))
	err := verifier.ParseYAMLStrict("a: [unclosed", "front-matter")
	require.Error(t, err)
	var defect *verifier.StructuralDefect
	require.ErrorAs(t, err, &defect)
	assert.Contains(t, err.Error(), "front-matter is not valid YAML: ")
	// The TS StructuralDefect class maps to docutil.CodeVerify (CONVENTIONS.md
	// "Errors": closest code, no new code invented).
	assert.Equal(t, docutil.CodeVerify, defect.Code())
}

/* --------------------------------- fail-closed edges (engine + measurement) */

func TestApplyOutcomes_NilManifestAllowlistIsAFailClosedAbstain(t *testing.T) {
	input := t1Input(t1ClaimFixPatch(t1ProseDoc), t1StrPtr(t1ProseDoc))
	input.ManifestAllowlist = nil // a misconfigured engine input cannot even decide
	result, err := verifier.RunTier1(input)
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	apply := t1CheckOf(t, result, "patch-applies-cleanly")
	assert.Equal(t, schemas.CheckFail, apply.Status)
	t1AssertDetailContains(t, apply, "application simulation could not decide (PatchEngineError)")
	t1AssertDetailContains(t, apply, "manifestAllowlist")
	// The permission re-checks still ran: an absent allowlist admits nothing.
	assert.Equal(t, schemas.CheckFail, t1CheckOf(t, result, "manifest-allowlist-only").Status)
	for _, id := range []string{
		"minimality-bound", "structural-integrity", "link-resolution", "examples-parse",
	} {
		assert.Equal(t, schemas.CheckSkipped, t1CheckOf(t, result, id).Status, "check %s", id)
	}
}

func TestRunTier1_EngineMeasurementFailurePropagatesAsAnError(t *testing.T) {
	// A non-finite bound is an engine-configuration failure: the simulation
	// cannot even measure, so the tier surfaces the error (TS: runTier1
	// rejects) instead of fabricating a record.
	input := t1Input(t1ClaimFixPatch(t1ProseDoc), t1StrPtr(t1ProseDoc), t1Override{
		minRatio: t1Float64p(-1),
	})
	_, err := verifier.RunTier1(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maxRatio must be a positive finite number")
}

func TestMinimalityBound_InvalidBoundOnAnAlreadyAppliedPatchFailsTheMeasurement(t *testing.T) {
	// The already-applied no-op path still measures: a malformed bound cannot
	// even be measured, so the bound check fails closed with the reason as
	// evidence instead of crashing the tier.
	alreadyPatched := strings.Replace(t1ProseDoc, t1PaymentsClaimLine, t1FixedClaimLine, 1)
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ProseDoc), t1StrPtr(alreadyPatched), t1Override{
		baseContent: t1StrPtr(t1ProseDoc),
		minRatio:    t1Float64p(-1),
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "patch-applies-cleanly").Status)
	bound := t1CheckOf(t, result, "minimality-bound")
	assert.Equal(t, schemas.CheckFail, bound.Status)
	t1AssertDetailContains(t, bound,
		"minimality measurement could not run: maxRatio must be a positive finite number, got -1")
	assert.Nil(t, result.Context.Minimality)
}

func TestLinkResolution_EscapingLinkFailsClosed(t *testing.T) {
	doc := t1LinksDoc("See [up](../../outside.md).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &t1LinkInputs,
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "link-resolution"),
		"link target '../../outside.md' does not stay inside the repository")
}

func TestLinkResolution_NilKnownDocsIndexFailsClosedWithZeroPaths(t *testing.T) {
	doc := t1LinksDoc("See [the guide](../guide.md).")
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(doc), t1StrPtr(doc), t1Override{
		t1LinkInputs: &verifier.LinkCheckInputs{},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "link-resolution"), "known-docs index has 0 path(s)")
}

func TestLinkResolution_RootLevelDocPathResolvesRelativeLinks(t *testing.T) {
	// A repo-root doc_path resolves relatives against the root itself
	// (posixDirname'' — the TS no-slash branch).
	rootDoc := t1LinksDoc("See [sibling](./sibling.md).")
	patchObject := t1BuildPatch(t1BuildPatchOpts{
		docPath:     "payments.md",
		baseContent: rootDoc,
		hunks: t1LineEdits(rootDoc, []t1LineEdit{
			{line: t1PaymentsClaimLine, replacement: t1FixedClaimLine},
		}),
	})
	result, err := verifier.RunTier1(t1Input(patchObject, t1StrPtr(rootDoc), t1Override{
		manifestAllowlist: map[string]bool{"payments.md": true},
		t1LinkInputs:      &t1LinkInputs,
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckPass, t1CheckOf(t, result, "patch-applies-cleanly").Status)
	links := t1CheckOf(t, result, "link-resolution")
	assert.Equal(t, schemas.CheckFail, links.Status)
	t1AssertDetailContains(t, links,
		"relative link './sibling.md' resolves to 'sibling.md' which is not a known doc")
}

func TestExamplesParse_CheckerPortErrorFailsTheCheck(t *testing.T) {
	result, err := verifier.RunTier1(t1Input(t1ClaimFixPatch(t1ExamplesDoc), t1StrPtr(t1ExamplesDoc), t1Override{
		exampleChecker: func(block verifier.ExampleBlock) (verifier.ExampleCheckResult, error) {
			if block.Language == "ts" {
				return verifier.ExampleCheckResult{}, fmt.Errorf("checker exploded on %s", block.Language)
			}
			return verifier.ExampleCheckResult{Ok: true}, nil
		},
	}))
	require.NoError(t, err)

	assert.Equal(t, schemas.CheckFail, result.Status)
	t1AssertDetailContains(t, t1CheckOf(t, result, "examples-parse"),
		"example checker threw on block tagged 'ts' at line 13: checker exploded on ts")
}
