package validate_test

// Ported from packages/core/test/validator.test.ts (V-P0-3, V-P0-8, V-P0-15,
// V-P0-6), validator-org-advisories-p12-t16.test.ts (F5-1/F5-3), and the
// fixture-manifest cases of fixtures.test.ts (P0-T17). Black-box: only the
// exported API is exercised.
//
// Scope note: the TS validator additionally runs the environment policy lint
// (E_ENV_POLICY), the repo walker, and manifest lint warnings
// (W_ZERO_MATCH_GLOB/W_NONEXISTENT_DOC_PATH/W_COVERAGE_LINT), plus the shared
// glob escape guard (E_GLOB_*). Those need core/src/envlint, core/src/lint and
// core/src/globs, which have no landed Go package yet — see internal/validate
// doc.go. Everything the validator DOES own (layout → manifest → mode
// resolution → org floors → diagnostics rendering) is ported here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingProbe is the no-op model stub (V-P0-8): only a real AI stage would
// call record(); nothing does — so Invoked() must stay 0.
type countingProbe struct{ calls int }

func (p *countingProbe) Invoked() int { return p.calls }

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		file := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
		require.NoError(t, os.WriteFile(file, []byte(content), 0o644))
	}
}

// writeBaseRepo writes the minimal canonical repo: manifest only (memory/
// state are presence-optional).
func writeBaseRepo(t *testing.T, manifestYAML string, files ...map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{".docsync/manifest.yml": manifestYAML}
	for _, extra := range files {
		for rel, content := range extra {
			all[rel] = content
		}
	}
	writeFiles(t, root, all)
	return root
}

const baseManifest = `version: 1
documents:
  - path: docs/payments.md
    mode: auto
    sources:
      - src/payments/**
`

var happyFiles = map[string]string{
	"docs/payments.md":       "# Payments\n",
	"docs/architecture.md":   "# Architecture\n",
	"docs/security.md":       "# Security\n",
	"src/payments/charge.ts": "export {};\n",
	"src/auth/guard.ts":      "export {};\n",
}

const exampleYAML = `version: 1
documents:
  - path: docs/payments.md
    mode: auto
    sources:
      - src/payments/**
    triggers:
      - public_behavior
      - api_change
    ignore:
      - internal_refactor
      - test_only
  - path: docs/architecture.md
    mode: suggest
    sources:
      - src/**
    triggers:
      - architecture_change
  - path: docs/security.md
    mode: locked
    sources:
      - src/auth/**
`

func errorWithCode(outcome validate.ValidationOutcome, code string) *validate.Diagnostic {
	for i := range outcome.Errors {
		if outcome.Errors[i].Code == code {
			return &outcome.Errors[i]
		}
	}
	return nil
}

// expectHardError asserts the code is present as an error, the run is
// fail-closed, and the AI-stage probe observed zero invocations.
func expectHardError(t *testing.T, outcome validate.ValidationOutcome, code string) *validate.Diagnostic {
	t.Helper()
	diagnostic := errorWithCode(outcome, code)
	require.NotNil(t, diagnostic, "expected a diagnostic with code %s", code)
	assert.Equal(t, validate.SeverityError, diagnostic.Severity)
	assert.False(t, outcome.OK)
	assert.Equal(t, 0, outcome.AIStageInvocations)
	return diagnostic
}

func TestLayoutViolationsAreHardErrorsWithZeroAIInvocations(t *testing.T) {
	t.Run("misplaced memory/policy-history/v1.json is reported with path precision", func(t *testing.T) {
		root := writeBaseRepo(t, baseManifest, map[string]string{
			".docsync/memory/policy-history/v1.json": "{}\n",
		})
		probe := &countingProbe{}
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: probe})
		require.NoError(t, err)

		assert.Equal(t, 0, probe.calls)
		assert.Equal(t, 0, outcome.AIStageInvocations)
		assert.False(t, outcome.OK)
		// The adapter reports both the stray directory and the stray file
		// beneath it.
		var misplaced []validate.Diagnostic
		for _, d := range outcome.Errors {
			if d.Code == "E_LAYOUT_MISPLACED_FILE" {
				misplaced = append(misplaced, d)
			}
		}
		require.GreaterOrEqual(t, len(misplaced), 1)
		foundFile, foundNested := false, false
		for _, d := range misplaced {
			if d.File == ".docsync/memory/policy-history/v1.json" {
				foundFile = true
			}
			if strings.Contains(d.Message, "nested location") {
				foundNested = true
			}
		}
		assert.True(t, foundFile, "expected a diagnostic at .docsync/memory/policy-history/v1.json")
		assert.True(t, foundNested, "expected a nested-location message")
	})

	t.Run("a missing .docsync/ dir yields the layout error plus a precise manifest-missing error", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"README.md": "# empty repo\n"})
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)

		expectHardError(t, outcome, "E_LAYOUT_MISSING_DOCSYNC_DIR")
		assert.Equal(t, ".docsync", errorWithCode(outcome, "E_LAYOUT_MISSING_DOCSYNC_DIR").File)
		expectHardError(t, outcome, "E_MANIFEST_MISSING")
		assert.Equal(t, ".docsync/manifest.yml", errorWithCode(outcome, "E_MANIFEST_MISSING").File)
	})

	t.Run("a manifest-less .docsync/ dir is missing_required_file (D7/D8)", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{".docsync/README.md": "hi\n"})
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)

		diagnostic := expectHardError(t, outcome, "E_LAYOUT_MISSING_REQUIRED_FILE")
		assert.Equal(t, ".docsync/manifest.yml", diagnostic.File)
	})
}

func TestEveryHardErrorClassHaltsWithTheRightDiagnostic(t *testing.T) {
	t.Run("E_MANIFEST_YAML: invalid YAML syntax", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_YAML")
		assert.Contains(t, diagnostic.Message, "invalid YAML")
		assert.Greater(t, diagnostic.Line, 0)
		assert.Nil(t, outcome.Manifest)
	})

	t.Run("E_MANIFEST_SCHEMA: unknown key is named with its own line (OD-R01, strict)", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources:
      - src/**
unknown_top: true
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_SCHEMA")
		assert.Contains(t, diagnostic.Message, `unknown key "unknown_top"`)
		assert.Equal(t, 6, diagnostic.Line)
		assert.Contains(t, diagnostic.Hint, "OD-R01")
	})

	t.Run("E_MANIFEST_UNKNOWN_MODE: bad mode with line-precise position (yaml AST mapping)", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    sources: [src/**]
    mode: auto2
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_UNKNOWN_MODE")
		assert.Equal(t, 5, diagnostic.Line)
		assert.Equal(t, 11, diagnostic.Column)
		assert.Equal(t, `unknown mode "auto2"`, diagnostic.Message)
		assert.Equal(t, "expected one of auto|suggest|locked|generated|human", diagnostic.Hint)
		require.NotEmpty(t, outcome.Rendered)
		assert.Contains(t, outcome.Rendered[0],
			"manifest.yml:5:11: [E_MANIFEST_UNKNOWN_MODE] unknown mode \"auto2\"")
	})

	t.Run("E_MANIFEST_MISSING_PATH: entry without a document path", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 1
documents:
  - mode: suggest
    sources:
      - src/**
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_MISSING_PATH")
		assert.Contains(t, diagnostic.Message, "document path is required")
		assert.Equal(t, 3, diagnostic.Line)
	})

	t.Run("E_MANIFEST_DUPLICATE_PATH: second occurrence is reported at its own line", func(t *testing.T) {
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
		assert.Equal(t, 5, diagnostic.Line)
	})

	t.Run("E_MANIFEST_UNSUPPORTED_VERSION: names the supported versions", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 2
documents:
  - path: docs/a.md
    sources: [src/**]
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_UNSUPPORTED_VERSION")
		assert.Contains(t, diagnostic.Message, "manifest version 2 is not supported")
		assert.Contains(t, diagnostic.Message, "supported versions: 1")
		assert.Equal(t, 1, diagnostic.Line)
	})

	t.Run("E_MANIFEST_FLOOR_CONFLICT: mode above org floor is an error, never a clamp (V-P0-6)", func(t *testing.T) {
		root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/security/policy.md
    mode: auto
    sources: [src/**]
`)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{
			Floors:    []schemas.OrgFloor{{Kind: schemas.FloorKindModeCeiling, Pattern: "docs/security/**", MaxMode: modePtr(schemas.DocModeSuggest)}},
			ModelStub: &countingProbe{},
		})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_FLOOR_CONFLICT")
		assert.Equal(t, 4, diagnostic.Line)
		assert.Contains(t, diagnostic.Message, `requests mode "auto"`)
		assert.Contains(t, diagnostic.Message, `exceeds org floor maximum "suggest"`)
		assert.Nil(t, outcome.EffectiveModes)
	})

	t.Run("E_MANIFEST_ORG_POLICY_INVALID: present-but-broken org policy is a hard error, never a silent drop", func(t *testing.T) {
		root := writeBaseRepo(t, baseManifest, map[string]string{
			".docsync/org-policy.yml": "version: 1\nfloors: [\n",
		})
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
		require.NoError(t, err)
		diagnostic := expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
		assert.Contains(t, diagnostic.Message, "refusing to run without the org policy")
		assert.Nil(t, outcome.Manifest)
	})
}

func TestFloorsFailClosedOnMalformedPatterns(t *testing.T) {
	// A malformed floor pattern is REJECTED, never silently non-matching:
	// a floor that fails open would be a security hole (D8).
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
	})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_FLOOR_CONFLICT")
	assert.Contains(t, diagnostic.Message, "invalid org floor configuration")
	assert.Contains(t, diagnostic.Message, "malformed floor pattern")
	assert.Nil(t, outcome.Manifest)
	assert.Nil(t, outcome.EffectiveModes)
}

func TestOverlappingModeCeilingFloorsComposeMostRestrictive(t *testing.T) {
	// Overlapping mode-ceiling floors compose to the MOST RESTRICTIVE
	// maximum; every violated (doc, floor) pair is reported (V-P0-6).
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
	assert.Nil(t, outcome.Manifest)
}

func TestOrgPolicySchemaViolationIsAHardError(t *testing.T) {
	// floors: [] violates the schema's min(1) — a PRESENT but invalid org
	// policy is a hard error (exit 2), never a silent drop (V-P12-12).
	root := writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": `version: 1
floors: []
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`,
	})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
	assert.Contains(t, diagnostic.Message, "violates docsync.org-policy/v1")
	assert.Contains(t, diagnostic.Message, "floors")
	assert.Nil(t, outcome.Manifest)
}

func TestOrgPolicyUnknownRootKeyIsRejected(t *testing.T) {
	root := writeBaseRepo(t, baseManifest, map[string]string{
		".docsync/org-policy.yml": `version: 1
rogue_key: true
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`,
	})
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_ORG_POLICY_INVALID")
	assert.Contains(t, diagnostic.Message, "rogue_key")
}

func TestDefaultsModeInvalidEnumIsSchemaError(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
defaults:
  mode: bogus
documents:
  - path: docs/a.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_SCHEMA")
	assert.Contains(t, diagnostic.Message, "defaults.mode")
	assert.Equal(t, 3, diagnostic.Line)
}

func TestInvalidDocumentPathIsSchemaErrorNotMissingPath(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: ../escape.md
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_SCHEMA")
	assert.Nil(t, errorWithCode(outcome, "E_MANIFEST_MISSING_PATH"))
	assert.Contains(t, diagnostic.Message, "documents.0.path")
	assert.Contains(t, diagnostic.Message, "nonempty relative POSIX path")
}

func TestEmptyDocumentPathValueIsMissingPath(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: ""
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	diagnostic := expectHardError(t, outcome, "E_MANIFEST_MISSING_PATH")
	assert.Contains(t, diagnostic.Message, "nonempty relative POSIX path")
	assert.Equal(t, 3, diagnostic.Line)
}

func TestGlobCanMatchUnderValidatorTightening(t *testing.T) {
	matrix := []struct {
		pattern   string
		forbidden []string
		expected  bool
	}{
		{"src/**", []string{".docsync"}, false},
		{"src/**", []string{".github", "workflows"}, false},
		{"state/**", []string{".docsync"}, false},
		{"docs/*.md", []string{".docsync"}, false},
		{"docs/*.md", []string{".github", "workflows"}, false},
		{".github/actions/**", []string{".github", "workflows"}, false},
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
	}
	for _, tc := range matrix {
		name := tc.pattern + " vs [" + strings.Join(tc.forbidden, "/") + "]"
		expected := tc.expected
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, expected, validate.GlobCanMatchUnder(tc.pattern, tc.forbidden))
		})
	}

	assert.Equal(t, validate.ForbiddenDocsyncPrefix, []string{".docsync"})
	assert.Equal(t, validate.ForbiddenWorkflowsPrefix, []string{".github", "workflows"})
}

func TestDeterminismIdenticalFixtureRendersIdentically(t *testing.T) {
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

func TestHappyPathValidManifestResolvesEffectiveModes(t *testing.T) {
	root := writeBaseRepo(t, exampleYAML, happyFiles)
	probe := &countingProbe{}
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: probe})
	require.NoError(t, err)

	assert.Equal(t, 0, probe.calls)
	assert.Equal(t, 0, outcome.AIStageInvocations)
	assert.True(t, outcome.OK)
	assert.Empty(t, outcome.Errors)
	assert.Empty(t, outcome.Warnings)
	assert.Empty(t, outcome.Rendered)
	require.NotNil(t, outcome.Manifest)

	// report-only built-in posture (D10): even "auto" cannot auto-apply in V1.
	assert.Equal(t, []validate.EffectiveModeSummary{
		{Path: "docs/payments.md", Mode: schemas.DocModeAuto, PatchCandidate: true, AutoApply: false},
		{Path: "docs/architecture.md", Mode: schemas.DocModeSuggest, PatchCandidate: true, AutoApply: false},
		{Path: "docs/security.md", Mode: schemas.DocModeLocked, PatchCandidate: false, AutoApply: false},
	}, outcome.EffectiveModes)

	// Exit-code-2 semantics: a fully valid configuration exits 0.
	assert.Equal(t, 0, outcome.ExitCode())
	assert.Equal(t, schemas.LearningModePropose, outcome.LearningModeDefault)
}

func TestFullDiagnosticListCollectsEveryErrorClass(t *testing.T) {
	// Multiple error classes in one pass (never first-error only): a layout
	// violation AND manifest schema errors are collected together.
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
	assert.Equal(t, 0, outcome.AIStageInvocations)

	// Fail-closed D8 with exit-code-2 semantics.
	assert.Equal(t, 2, outcome.ExitCode())
	assert.Nil(t, outcome.Manifest)
}

func TestErrorsSortDeterministically(t *testing.T) {
	root := writeBaseRepo(t, `version: 1
documents:
  - path: docs/a.md
    mode: auto2
    sources: [src/**]
  - mode: suggest
    sources: [src/**]
`)
	outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{ModelStub: &countingProbe{}})
	require.NoError(t, err)

	type key struct {
		file string
		line int
		code string
	}
	keys := make([]key, len(outcome.Errors))
	for i, d := range outcome.Errors {
		keys[i] = key{d.File, d.Line, d.Code}
	}
	for i := 1; i < len(keys); i++ {
		prev, cur := keys[i-1], keys[i]
		assert.True(t,
			prev.file < cur.file ||
				(prev.file == cur.file && prev.line < cur.line) ||
				(prev.file == cur.file && prev.line == cur.line && prev.code <= cur.code),
			"errors must be sorted by (file, line, code): %v then %v", prev, cur)
	}
}

// ---------------------------------------------------------- org advisories

const advisoriesManifestYAML = `version: 1
defaults:
  mode: suggest
documents:
  - path: docs/readme.md
    mode: auto
    sources:
      - "src/**"
`

const orgPolicyUnreviewedYAML = `version: 1
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - security-team
learning:
  defaultMode: canary
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`

const orgPolicyReviewedYAML = `version: 1
floors:
  - kind: report_only_until_reviewed
    pattern: docs/**
    requiredReviewers:
      - security-team
    reviewed: true
learning:
  defaultMode: canary
createdAt: "2026-01-01T00:00:00Z"
updatedAt: "2026-01-01T00:00:00Z"
`

var advisoriesFiles = map[string]string{
	"docs/readme.md": "# Readme\n",
	"src/index.ts":   "export {};\n",
}

func writeAdvisoriesRepo(t *testing.T, orgPolicyYAML string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{".docsync/manifest.yml": advisoriesManifestYAML}
	if orgPolicyYAML != "" {
		files[".docsync/org-policy.yml"] = orgPolicyYAML
	}
	for rel, content := range advisoriesFiles {
		files[rel] = content
	}
	writeFiles(t, root, files)
	return root
}

func TestValidatorCarriesReportOnlyAdvisories(t *testing.T) {
	t.Run("an unreviewed report_only_until_reviewed floor surfaces as a non-blocking advisory", func(t *testing.T) {
		root := writeAdvisoriesRepo(t, orgPolicyUnreviewedYAML)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
		require.NoError(t, err)

		// Advisory — never a halt: the run stays valid (V-P12-12).
		assert.True(t, outcome.OK)
		assert.Empty(t, outcome.Errors)
		require.Len(t, outcome.Advisories, 1)
		advisory := outcome.Advisories[0]
		assert.Equal(t, "docs/readme.md", advisory.Path)
		assert.Contains(t, advisory.Message, "manifest not yet reviewed")
		assert.Contains(t, advisory.Message, "docs/**")
		// The floor kind is preserved for renderers that want structured detail.
		assert.Equal(t, schemas.FloorKindReportOnlyUntilReviewed, advisory.Floor.Kind)
	})

	t.Run("a reviewed: true declaration silences the advisory (absence is not a review)", func(t *testing.T) {
		root := writeAdvisoriesRepo(t, orgPolicyReviewedYAML)
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
		require.NoError(t, err)

		assert.True(t, outcome.OK)
		assert.Empty(t, outcome.Advisories)
	})

	t.Run("a repo without .docsync/org-policy.yml carries no advisories", func(t *testing.T) {
		root := writeAdvisoriesRepo(t, "")
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
		require.NoError(t, err)

		assert.True(t, outcome.OK)
		assert.Empty(t, outcome.Advisories)
		assert.Equal(t, schemas.LearningModePropose, outcome.LearningModeDefault)
	})
}

func TestOrgLearningDefaultReachesTheResult(t *testing.T) {
	root := writeAdvisoriesRepo(t, "")
	baseline, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	root = writeAdvisoriesRepo(t, orgPolicyUnreviewedYAML)
	withPolicy, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
	require.NoError(t, err)

	assert.True(t, withPolicy.OK)
	// The org default now flows (previously inert — no caller passed orgPolicy).
	assert.Equal(t, schemas.LearningModeCanary, withPolicy.LearningModeDefault)
	assert.Equal(t, schemas.LearningModePropose, baseline.LearningModeDefault)
	// …and it changes NOTHING else: modes, patch candidacy and auto-apply are
	// identical to the no-policy run (INV-4 defense in depth — the org
	// learning default is a learning-posture default, never a mode override).
	assert.Equal(t, baseline.EffectiveModes, withPolicy.EffectiveModes)
	assert.Equal(t, []validate.EffectiveModeSummary{
		{Path: "docs/readme.md", Mode: schemas.DocModeAuto, PatchCandidate: true, AutoApply: false},
	}, withPolicy.EffectiveModes)
}

// ---------------------------------------------------------- fixture manifests

func TestFixtureManifestsParseAndValidate(t *testing.T) {
	t.Run("payments manifest parses and validates", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "payments", ".docsync", "manifest.yml"))
		require.NoError(t, err)
		manifest, err := schemas.ParseManifest(data)
		require.NoError(t, err)

		assert.Equal(t, 1, manifest.Version)
		paths := make([]string, len(manifest.Documents))
		for i, doc := range manifest.Documents {
			paths[i] = doc.Path
		}
		assert.Equal(t, []string{"docs/payments.md", "docs/architecture.md", "docs/security.md"}, paths)
		payments := manifest.Documents[0]
		require.NotNil(t, payments.Mode)
		assert.Equal(t, schemas.DocModeAuto, *payments.Mode)
		assert.Equal(t, []string{"src/payments/**", "src/api/payments/**"}, payments.Sources)
		assert.Equal(t, []string{"public_behavior", "api_change"}, payments.Triggers)
		assert.Equal(t, []string{"test_only", "internal_refactor"}, payments.Ignore)
	})

	t.Run("docs-heavy manifest validates and maps exactly 6 of the 8 docs", func(t *testing.T) {
		root := filepath.Join("testdata", "docs-heavy")
		data, err := os.ReadFile(filepath.Join(root, ".docsync", "manifest.yml"))
		require.NoError(t, err)
		manifest, err := schemas.ParseManifest(data)
		require.NoError(t, err)

		require.NotNil(t, manifest.Defaults)
		require.NotNil(t, manifest.Defaults.Mode)
		assert.Equal(t, schemas.DocModeSuggest, *manifest.Defaults.Mode)
		assert.Len(t, manifest.Documents, 6)

		docs := listMarkdownDocs(t, filepath.Join(root, "docs"))
		// Repo-relative, matching the manifest allowlist.
		for i := range docs {
			docs[i] = "docs/" + docs[i]
		}
		assert.Len(t, docs, 8)
		// The manifest is an allowlist: every mapped doc exists on disk; the
		// two remaining docs are deliberately unmapped (D7).
		mapped := map[string]bool{}
		for _, doc := range manifest.Documents {
			mapped[doc.Path] = true
		}
		assert.Len(t, mapped, 6)
		for path := range mapped {
			assert.Contains(t, docs, path)
		}
		// Through the full validator, the fixture repository is canonical and
		// fully valid (fail-closed gate: no hard errors, exit 0).
		outcome, err := validate.ValidateRepositoryConfig(root, validate.ValidateOptions{})
		require.NoError(t, err)
		assert.True(t, outcome.OK)
		assert.Empty(t, outcome.Errors)
		assert.Equal(t, 0, outcome.ExitCode())
		assert.Len(t, outcome.EffectiveModes, 6)
	})

	t.Run("monorepo-scale manifest validates with one suggest entry per package", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("testdata", "monorepo-scale", ".docsync", "manifest.yml"))
		require.NoError(t, err)
		manifest, err := schemas.ParseManifest(data)
		require.NoError(t, err)

		require.Len(t, manifest.Documents, 8)
		for i, doc := range manifest.Documents {
			require.NotNil(t, doc.Mode)
			assert.Equal(t, schemas.DocModeSuggest, *doc.Mode)
			assert.Equal(t, []string{"architecture_change"}, doc.Triggers)
			assert.Equal(t, []string{"packages/" + string(rune('a'+i)) + "/src/**"}, doc.Sources)
		}
	})
}

func listMarkdownDocs(t *testing.T, dir string) []string {
	t.Helper()
	var docs []string
	require.NoError(t, filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		docs = append(docs, filepath.ToSlash(rel))
		return nil
	}))
	return docs
}

func modePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }
