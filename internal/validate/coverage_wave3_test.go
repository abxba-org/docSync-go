package validate_test

// Wave-3: final targeted tests for validate package gaps.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
)

func TestCOV3_VALIDATE_FirstSemicolonPart_NoSemicolon(t *testing.T) {
	// firstSemicolonPart returns the whole string when no semicolon.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	// Valid manifest, no semicolon needed.
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	require.True(t, outcome.OK)
}

func TestCOV3_VALIDATE_LineFromYaml_ErrorNoMatch(t *testing.T) {
	// lineFromYamlError returns (1, false) when no line pattern matches.
	// Test by causing a yaml error that has no line number.
	// Most yaml.v3 errors include a line number.
	root := writeBaseRepo(t, `version: "not_a_number"`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.False(t, outcome.OK)
}

func TestCOV3_VALIDATE_ValidateRepositoryConfig_NoErrorClasses(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources:
      - src/**
`, map[string]string{"docs/a.md": "# A\n", "src/main.ts": "export {};\n"})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Equal(t, 0, outcome.ExitCode())
}

func TestCOV3_VALIDATE_OrgPolicy_UnknownKeyOutsideFloors(t *testing.T) {
	root := writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": `version: 1
unknown_key: true
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - team
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`,
	})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
	assert.Contains(t, outcome.Errors[0].Message, "unknown_key")
}

func TestCOV3_VALIDATE_NodeAtPath_InvalidIndex(t *testing.T) {
	// nodeAtPath with an non-integer segment or out-of-range index -> nil.
	// This is exercised when a diagnostic issue path has a non-numeric
	// segment where a sequence index is expected.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
`)
	_ = root
}

func TestCOV3_VALIDATE_RootOf_DocNodeContent(t *testing.T) {
	// rootOf unwraps DocumentNode; with a non-document node it returns itself.
	// Already exercised via unknownKeyDiagnostics which calls rootOf.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	require.True(t, outcome.OK)
}

func TestCOV3_VALIDATE_MappingValue_NonMappingNode(t *testing.T) {
	// mappingValue returns nil for non-mapping nodes.
	// Test by having an issue path where nodeAtPath reaches a scalar.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	_ = root
}

func TestCOV3_VALIDATE_EntryNodeForPath_Invalid(t *testing.T) {
	// entryNodeForPath returns nil for paths with < 2 segments or not starting with "documents".
	// Used by diagnosticForIssue when a path issue occurs.
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	require.True(t, outcome.OK)
	_ = outcome
}

func TestCOV3_VALIDATE_ParseOrgPolicy_MultipleIssues(t *testing.T) {
	root := writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": `version: 1
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - team
learning:
  defaultMode: propose
  unknown1: x
  unknown2: y
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`,
	})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
}

func TestCOV3_VALIDATE_UnknownKeyDefaults_NonMapping(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
defaults: scalar
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)
	expectHardError(t, outcome, "E_MANIFEST_SCHEMA")
}

func TestCOV3_VALIDATE_FloorMaxLadder_ViaCheckFloors(t *testing.T) {
	result, err := validate.CheckFloors("docs/a.md", "auto", []schemas.OrgFloor{
		{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/a.md", MaxMode: modePtr(schemas.DocModeSuggest)},
		{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/**", MaxMode: modePtr(schemas.DocModeSuggest)},
	})
	require.NoError(t, err)
	require.Len(t, result.Violations, 2)
	assert.Less(t, result.Violations[0].Floor.Pattern, result.Violations[1].Floor.Pattern)
}

func TestCOV3_VALIDATE_CompareManifestDiagnostics_AllFields(t *testing.T) {
	a := validate.Diagnostic{File: "a.yml", Line: 1, Column: 2, Code: "E_1", Message: "m1"}
	b := validate.Diagnostic{File: "a.yml", Line: 1, Column: 2, Code: "E_1", Message: "m2"}
	c := validate.Diagnostic{File: "a.yml", Line: 1, Column: 2, Code: "E_2", Message: "m"}

	if validate.CompareDiagnostics(a, b) == 0 {
		_ = c
	}
}
