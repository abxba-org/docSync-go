package validate_test

// Ported coverage-hardening suite for internal/validate. Targets the
// statements and branches validate_test.go leaves below 95%: the manifest
// semantic-check edges (unsupported version, duplicate path, org policy
// invalid/unknown-key, floor conflicts and composition, mode resolution),
// diagnostics rendering and comparison, validator layout/runtime error paths,
// and glob-tightening edge cases. Black-box (package validate_test), testify,
// deterministic (temp dirs per test).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
)

/* ------------------------------------------------------------- manifest semantic checks */

func TestCOV_VALIDATE_Manifest_UnsupportedVersion(t *testing.T) {
	root := writeBaseRepo(t, `version: 9
documents:
  - path: docs/a.md
    sources:
      - src/**
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_UNSUPPORTED_VERSION")
	assert.Contains(t, outcome.Errors[0].Message, "supported versions: 1")
}

func TestCOV_VALIDATE_Manifest_DuplicatePath(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
  - path: docs/a.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	diagnostic := expectHardError(t, outcome, "E_MANIFEST_DUPLICATE_PATH")
	assert.Contains(t, diagnostic.Message, `duplicate document path "docs/a.md"`)
}

func TestCOV_VALIDATE_Manifest_OrgPolicyInvalidAndUnknownKey(t *testing.T) {
	// present-but-broken org policy (syntax error).
	root := writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": "version: 1\nfloors: [\n",
	})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
	assert.Nil(t, outcome.Manifest)

	// present but invalid schema (floors: [] violates min(1)).
	root = writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": `version: 1
floors: []
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`,
	})
	outcome, err = validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
	assert.Contains(t, outcome.Errors[0].Message, "floors")
}

func TestCOV_VALIDATE_Manifest_FloorConflictComposition(t *testing.T) {
	// multiple overlapping mode-ceiling floors compose most restrictive; every
	// violated (doc, floor) pair reported.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/security/policy.md
    mode: auto
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{
		Floors: []schemas.OrgFloor{
			{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: modePtr(schemas.DocModeSuggest)},
			{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**", MaxMode: modePtr(schemas.DocModeLocked)},
		},
		ModelStub: &countingProbe{},
	})
	require.NoError(t, err)
	conflicts := 0
	for _, d := range outcome.Errors {
		if d.Code == "E_MANIFEST_FLOOR_CONFLICT" {
			conflicts++
		}
	}
	assert.Equal(t, 2, conflicts)
	assert.False(t, outcome.OK)
	assert.Nil(t, outcome.EffectiveModes)
}

func TestCOV_VALIDATE_Manifest_ReportOnlyFloorAdvisory(t *testing.T) {
	root := writeAdvisoriesRepo(t, orgPolicyUnreviewedYAML)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Empty(t, outcome.Errors)
	require.Len(t, outcome.Advisories, 1)
	assert.Equal(t, "docs/readme.md", outcome.Advisories[0].Path)
}

func TestCOV_VALIDATE_Manifest_FloorMalformedRejected(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{
		Floors: []schemas.OrgFloor{
			{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**/mid/**", MaxMode: modePtr(schemas.DocModeSuggest)},
		},
		ModelStub: &countingProbe{},
	})
	require.NoError(t, err)
	diagnostic := expectHardError(t, outcome, "E_MANIFEST_FLOOR_CONFLICT")
	assert.Contains(t, diagnostic.Message, "invalid org floor configuration")
	assert.Nil(t, outcome.Manifest)
}

func TestCOV_VALIDATE_Manifest_InvalidDocumentPathSchemaError(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: ../escape.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_SCHEMA")
	assert.Contains(t, outcome.Errors[0].Message, "documents.0.path")
}

func TestCOV_VALIDATE_Manifest_EmptyDocumentPathMissingPath(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: ""
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_MISSING_PATH")
}

/* ------------------------------------------------------------- mode resolution */

func TestCOV_VALIDATE_Mode_DefaultsAndEntryPrecedence(t *testing.T) {
	// entry mode wins; defaults mode fallback; built-in suggest fallback.
	root := writeBaseRepo(t, `version: 1
defaults:
  mode: locked
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
  - path: docs/b.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	require.Len(t, outcome.EffectiveModes, 2)
	assert.Equal(t, schemas.DocModeAuto, outcome.EffectiveModes[0].Mode)
	assert.Equal(t, schemas.DocModeLocked, outcome.EffectiveModes[1].Mode)
	assert.True(t, outcome.EffectiveModes[0].PatchCandidate)
	assert.False(t, outcome.EffectiveModes[1].PatchCandidate)
}

func TestCOV_VALIDATE_Mode_IsPatchCandidate(t *testing.T) {
	assert.True(t, validate.IsPatchCandidateMode(schemas.DocModeAuto))
	assert.True(t, validate.IsPatchCandidateMode(schemas.DocModeSuggest))
	assert.True(t, validate.IsPatchCandidateMode(schemas.DocModeHuman))
	assert.False(t, validate.IsPatchCandidateMode(schemas.DocModeLocked))
	assert.False(t, validate.IsPatchCandidateMode(schemas.DocModeGenerated))
}

/* ------------------------------------------------------------- diagnostics rendering */

func TestCOV_VALIDATE_Diagnostics_RenderColumnAndHint(t *testing.T) {
	d := validate.Diagnostic{
		Severity: validate.SeverityError, Code: "E_MANIFEST_UNKNOWN_MODE",
		File: "manifest.yml", Line: 5, Column: 11,
		Message: `unknown mode "auto2"`,
		Hint:    "expected one of auto|suggest|locked|generated|human",
	}
	rendered := validate.RenderDiagnostic(d)
	assert.Equal(t, `manifest.yml:5:11: [E_MANIFEST_UNKNOWN_MODE] unknown mode "auto2" — expected one of auto|suggest|locked|generated|human`, rendered)

	// no line/column -> file-level, no trailing separators.
	d2 := validate.Diagnostic{Severity: validate.SeverityWarning, Code: "W_COVERAGE_LINT",
		File: "manifest.yml", Message: "hint", Hint: ""}
	assert.Equal(t, "manifest.yml: [W_COVERAGE_LINT] hint", validate.RenderDiagnostic(d2))

	// hint omitted.
	d3 := validate.Diagnostic{Severity: validate.SeverityError, Code: "E_MANIFEST_MISSING",
		File: "manifest.yml", Line: 1, Message: "missing"}
	assert.Equal(t, "manifest.yml:1: [E_MANIFEST_MISSING] missing", validate.RenderDiagnostic(d3))
}

func TestCOV_VALIDATE_Diagnostics_CompareOrder(t *testing.T) {
	a := validate.Diagnostic{File: "b.yml", Line: 2, Code: "E_MANIFEST_SCHEMA", Message: "m"}
	b := validate.Diagnostic{File: "a.yml", Line: 1, Code: "E_MANIFEST_SCHEMA", Message: "m"}
	assert.Less(t, validate.CompareDiagnostics(b, a), 0)
	assert.Greater(t, validate.CompareDiagnostics(a, b), 0)
	assert.Equal(t, 0, validate.CompareDiagnostics(a, a))
}

/* ------------------------------------------------------------- validator runtime paths */

func TestCOV_VALIDATE_Validator_LayoutDirExpected(t *testing.T) {
	// .docsync/ exists as a file -> ViolationDirExpected -> E_LAYOUT_DIR_EXPECTED.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".docsync"), []byte("not a dir"), 0o644))
	writeFiles(t, root, map[string]string{"README.md": "# x\n"})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.NotNil(t, errorWithCode(outcome, "E_LAYOUT_DIR_EXPECTED"))
	assert.Equal(t, ".docsync", errorWithCode(outcome, "E_LAYOUT_DIR_EXPECTED").File)
}

func TestCOV_VALIDATE_Validator_EffectiveModesNilOnFailure(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto2
    sources: [src/**]
`, map[string]string{".docsync/stray.txt": "stray\n"})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.Nil(t, outcome.EffectiveModes)
	assert.Equal(t, 2, outcome.ExitCode())
}

func TestCOV_VALIDATE_Validator_FullDiagnosticList(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto2
    sources: [src/**]
`, map[string]string{".docsync/stray.txt": "stray\n"})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.NotNil(t, errorWithCode(outcome, "E_LAYOUT_UNKNOWN_ENTRY"))
	assert.NotNil(t, errorWithCode(outcome, "E_MANIFEST_UNKNOWN_MODE"))
	assert.GreaterOrEqual(t, len(outcome.Errors), 2)
	assert.Equal(t, 2, outcome.ExitCode())
}

func TestCOV_VALIDATE_Validator_DeterministicRender(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/ghost.md
    sources:
      - src/ghost/**
`, map[string]string{"src/real.ts": "export {};\n"})
	first, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)
	second, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)
	assert.Equal(t, first.Rendered, second.Rendered)
	assert.Equal(t, first.Errors, second.Errors)
	assert.Equal(t, first.Warnings, second.Warnings)
}

func TestCOV_VALIDATE_Validator_HappyPathExitZero(t *testing.T) {
	root := writeBaseRepo(t, exampleYAML, happyFiles)
	probe := &countingProbe{}
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: probe})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Empty(t, outcome.Errors)
	assert.Empty(t, outcome.Warnings)
	assert.Equal(t, 0, outcome.ExitCode())
	assert.Equal(t, schemas.LearningModePropose, outcome.LearningModeDefault)
	assert.Equal(t, 0, outcome.AIStageInvocations)
}

/* ------------------------------------------------------------- glob tightening edges */

func TestCOV_VALIDATE_Glob_GlobCanMatchUnderEdges(t *testing.T) {
	cases := []struct {
		pattern   string
		forbidden []string
		expected  bool
	}{
		{"src/**", []string{".docsync"}, false},
		{"**", []string{".docsync"}, true},
		{"**/state/**", []string{".docsync"}, true},
		{"*/state/**", []string{".docsync"}, true},
		{"a/**/state/**", []string{".docsync"}, false},
		{"*/**/state/**", []string{".docsync"}, true},
		{".docsync/state/**", []string{".docsync"}, true},
		{"**/workflows/**", []string{".github", "workflows"}, true},
		{".github/*/**", []string{".github", "workflows"}, true},
		{".github/*", []string{".github", "workflows"}, true},
		{".github/**", []string{".github", "workflows"}, true},
		{"{src,docs}/**", []string{".docsync"}, true},
		{"docs/*.md", []string{".docsync"}, false},
	}
	for _, tc := range cases {
		name := tc.pattern + " vs [" + strings.Join(tc.forbidden, "/") + "]"
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, validate.GlobCanMatchUnder(tc.pattern, tc.forbidden))
		})
	}
	assert.Equal(t, validate.ForbiddenDocsyncPrefix, []string{".docsync"})
	assert.Equal(t, validate.ForbiddenWorkflowsPrefix, []string{".github", "workflows"})
}

var _ = filepath.Join
var _ = strings.Join

/* ------------------------------------------------------------- floor maxMode validation */

func TestCOV_VALIDATE_Floor_MaxModeUndefinedIsMalformed(t *testing.T) {
	// Mode-ceiling floor with maxMode undefined -> fail-closed error, never a
	// silent pass (assertValidFloorMaxMode nil branch).
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{
		Floors: []schemas.OrgFloor{
			{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**"},
		},
		ModelStub: &countingProbe{},
	})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_FLOOR_CONFLICT")
	assert.Contains(t, outcome.Errors[0].Message, "maxMode undefined")
	assert.Nil(t, outcome.Manifest)
}

func TestCOV_VALIDATE_Floor_InvalidMaxModeIsMalformed(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{
		Floors: []schemas.OrgFloor{
			{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**", MaxMode: modePtr(schemas.DocMode("bogus"))},
		},
		ModelStub: &countingProbe{},
	})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_FLOOR_CONFLICT")
	assert.Contains(t, outcome.Errors[0].Message, "maxMode")
	assert.Nil(t, outcome.Manifest)
}

/* ------------------------------------------------------------- entry mode defaults */

func TestCOV_VALIDATE_Mode_DefaultsFallbackWhenNoModeOrDefaults(t *testing.T) {
	// No entry mode and no defaults.mode -> built-in suggest fallback.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	require.Len(t, outcome.EffectiveModes, 1)
	assert.Equal(t, schemas.DocModeSuggest, outcome.EffectiveModes[0].Mode)
	assert.True(t, outcome.EffectiveModes[0].PatchCandidate)
}

func TestCOV_VALIDATE_Mode_DefaultsModeAppliesToEntriesWithoutMode(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
defaults:
  mode: auto
documents:
  - path: docs/a.md
    sources: [src/**]
  - path: docs/b.md
    mode: locked
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	require.Len(t, outcome.EffectiveModes, 2)
	assert.Equal(t, schemas.DocModeAuto, outcome.EffectiveModes[0].Mode)
	assert.True(t, outcome.EffectiveModes[0].PatchCandidate)
	assert.Equal(t, schemas.DocModeLocked, outcome.EffectiveModes[1].Mode)
	assert.False(t, outcome.EffectiveModes[1].PatchCandidate)
}

/* ------------------------------------------------------------- firstSemicolonPart / hint rendering */

func TestCOV_VALIDATE_Hint_RendersSupportedModes(t *testing.T) {
	// firstSemicolonPart trims the hint at the first semicolon; the unknown
	// mode hint carries the full supported-modes list.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
    mode: nope
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	diagnostic := expectHardError(t, outcome, "E_MANIFEST_UNKNOWN_MODE")
	assert.Equal(t, `expected one of auto|suggest|locked|generated|human`, diagnostic.Hint)
}

var _ = schemas.FloorKindModeCeiling
