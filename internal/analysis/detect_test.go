package analysis_test

// Ported from packages/core/test/analysis-detect.test.ts (P1-T03/P1-T04):
// pure test-only / formatting-only / generated detection, deterministic hint
// precedence, the V-P1-3 formatting-only sweep short-circuit, and a static
// purity audit of the analysis package's imports. Real git only for the
// V-P1-3 sweep; all other cases are pure function tables.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/analysis"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* --------------------------------------------------------- isTestOnlyFile */

// TestIsTestOnlyFile (P1-T03): name conventions match at any depth and
// top-level test directories; nested literal `test/` directories do NOT
// (conservative, documented under-matching).
func TestIsTestOnlyFile(t *testing.T) {
	positive := []string{
		"refund.test.ts",
		"src/payments/refund.test.ts",
		"src/payments/refund.spec.ts",
		"a/b/c.spec.tsx",
		"src/__tests__/helpers.ts",
		"__tests__/x.js",
		"test/e2e.js",
		"tests/unit/a.py",
		"spec/b.rb",
		"e2e/c.ts",
	}
	for _, path := range positive {
		t.Run("positive/"+path, func(t *testing.T) {
			assert.True(t, analysis.IsTestOnlyFile(path), path)
		})
	}
	negative := []string{
		"src/main.ts",
		"src/test-utils.ts",
		"src/testing.mts",
		"packages/core/test/a.ts", // nested test dir: literal top-level reading, conservative
		"docs/testing.md",
	}
	for _, path := range negative {
		t.Run("negative/"+path, func(t *testing.T) {
			assert.False(t, analysis.IsTestOnlyFile(path), path)
		})
	}
}

/* ---------------------------------------------------- isFormattingOnlyFile */

// hunk builds a synthetic unified-diff hunk (TS test helper `hunk`; the
// optional `context` default is variadic here).
func hunk(removed, added []string, context ...string) string {
	lines := make([]string, 0, len(context)+len(removed)+len(added))
	for _, line := range context {
		lines = append(lines, " "+line)
	}
	for _, line := range removed {
		lines = append(lines, "-"+line)
	}
	for _, line := range added {
		lines = append(lines, "+"+line)
	}
	return "diff --git a/f.txt b/f.txt\nindex 111..222 100644\n--- a/f.txt\n+++ b/f.txt\n@@ -1,3 +1,3 @@\n" +
		strings.Join(lines, "\n")
}

// hunkOf builds the minimal single-hunk diff used by the hint-precedence
// table (TS test helper `hunkOf`).
func hunkOf(removed, added []string) string {
	lines := make([]string, 0, len(removed)+len(added))
	for _, line := range removed {
		lines = append(lines, "-"+line)
	}
	for _, line := range added {
		lines = append(lines, "+"+line)
	}
	return "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,1 +1,1 @@\n" + strings.Join(lines, "\n")
}

// detectStrPtr returns a pointer to s, for the optional DetectorFile
// members in test fixtures. Prefixed against sibling black-box test helpers
// in package analysis_test.
func detectStrPtr(s string) *string { return &s }

func TestIsFormattingOnlyFile(t *testing.T) {
	t.Run("whitespace-only paired line changes are formatting-only", func(t *testing.T) {
		assert.True(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"const a = 1;"}, []string{"const a = 1;   "}),
		}))
		assert.True(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"  x = y"}, []string{"\tx = y"}),
		}))
		assert.True(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"a", "b"}, []string{"   a", "b   "}),
		}))
		assert.True(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"one"}, []string{"one"}, "ctx"),
		}))
	})

	t.Run("content changes, count mismatches, and internal whitespace shifts are not formatting-only", func(t *testing.T) {
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"foo"}, []string{"bar"}),
		}))
		// Trimmed equality is END-only: internal spacing differences stay real
		// changes.
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"a  b"}, []string{"a b"}),
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"a"}, []string{"a", "b"}),
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"a", "b"}, []string{"b"}),
		}))
		// One good hunk does not rescue a bad one.
		twoHunks := hunk([]string{"x  "}, []string{"x"}) + "\n@@ -9,1 +9,1 @@\n-real\n+changed\n"
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Patch: twoHunks}))
	})

	t.Run("added, deleted, rename-without-hunks, mode-only, binary, and empty patches are not formatting-only", func(t *testing.T) {
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk(nil, []string{"brand new file"}),
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: hunk([]string{"old file"}, nil),
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: "diff --git a/old.md b/new.md\nsimilarity index 100%\nrename from old.md\nrename to new.md",
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: "diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755",
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{
			Patch: "diff --git a/img.bin b/img.bin\nBinary files a/img.bin and b/img.bin differ",
		}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{Patch: ""}))
		assert.False(t, analysis.IsFormattingOnlyFile(analysis.DetectorFile{}))
	})
}

/* -------------------------------------------------------- isGeneratedFile */

func TestIsGeneratedFile(t *testing.T) {
	t.Run("path conventions match at any depth", func(t *testing.T) {
		positive := []string{
			"src/api/generated/client.ts",
			"generated/client.ts",
			"src/__generated__/queries.ts",
			"src/x.gen.ts",
			"types/x.gen.js",
			"dist/bundle.js",
			"build/out.js",
			"app.min.js",
			"web/dist/a/b.js",
		}
		for _, path := range positive {
			t.Run("positive/"+path, func(t *testing.T) {
				assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{Path: path}), path)
			})
		}
		assert.False(t, analysis.IsGeneratedFile(analysis.DetectorFile{Path: "src/generated2/x.ts"}))
		assert.False(t, analysis.IsGeneratedFile(analysis.DetectorFile{Path: "src/app.ts"}))
	})

	t.Run("content markers count ONLY within the first 10 lines and only when content is provided", func(t *testing.T) {
		marker := "src/app.ts"
		assert.False(t, analysis.IsGeneratedFile(analysis.DetectorFile{Path: marker}))
		assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr("// @generated by protoc\nexport {}\n"),
		}))
		assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr("// DO NOT EDIT\nexport {}\n"),
		}))
		assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr("// do not edit by hand\nexport {}\n"),
		}))
		assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr("// Automatically generated file\n"),
		}))
		plainLines := func(n int) string {
			lines := make([]string, 0, n)
			for i := 1; i <= n; i++ {
				lines = append(lines, "// line "+strconv.Itoa(i))
			}
			return strings.Join(lines, "\n")
		}
		// Marker on line 11 → outside the 10-line window.
		assert.False(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr(plainLines(10) + "\n// DO NOT EDIT\n"),
		}))
		// Marker on line 10 → inside the window (inclusive).
		assert.True(t, analysis.IsGeneratedFile(analysis.DetectorFile{
			Path:          marker,
			ContentAtHead: detectStrPtr(plainLines(9) + "\n// DO NOT EDIT\n"),
		}))
		assert.False(t, analysis.IsGeneratedFile(analysis.DetectorFile{Path: marker, ContentAtHead: detectStrPtr("")}))
	})
}

/* ------------------------------------------------ hint precedence (pure) */

// flagsFor maps paths through detectFileFlags with no patch (TS `flagsFor`).
func flagsFor(paths []string) []analysis.DetectedFileFlags {
	out := make([]analysis.DetectedFileFlags, 0, len(paths))
	for _, path := range paths {
		out = append(out, analysis.DetectFileFlags(analysis.DetectorFile{Path: path}))
	}
	return out
}

// TestDeriveClassificationHintsPrecedence (P1-T03/T04): the fixed
// short-circuit precedence — generated wins over everything, then test_only,
// then formatting_only; mixed/empty → no hint.
func TestDeriveClassificationHintsPrecedence(t *testing.T) {
	t.Run("test-only change with a generated file → generated hint, no test_only short-circuit", func(t *testing.T) {
		hints := analysis.DeriveClassificationHints(
			flagsFor([]string{"src/a.test.ts", "src/api/client.gen.ts"}))
		require.Len(t, hints, 1)
		assert.Equal(t, schemas.ClassificationHintGenerated, hints[0].Kind)
		assert.Contains(t, hints[0].Note, analysis.InspectGeneratedOutputAction)
	})

	t.Run("all test-only → test_only hint (verbatim note), even when also formatting-only", func(t *testing.T) {
		hints := analysis.DeriveClassificationHints(
			flagsFor([]string{"src/a.test.ts", "tests/b.ts", "__tests__/c.ts"}))
		assert.Equal(t, []schemas.ClassificationHint{{
			Kind: schemas.ClassificationHintTestOnly,
			Note: "all changed paths match test patterns — skip Jev entirely (R10)",
		}}, hints)
	})

	t.Run("all formatting-only → formatting_only hint (deterministic note)", func(t *testing.T) {
		hints := analysis.DeriveClassificationHints([]analysis.DetectedFileFlags{
			analysis.DetectFileFlags(analysis.DetectorFile{
				Path:  "src/a.ts",
				Patch: hunkOf([]string{"x  "}, []string{"x"}),
			}),
			analysis.DetectFileFlags(analysis.DetectorFile{
				Path:  "src/b.ts",
				Patch: hunkOf([]string{"y"}, []string{" y"}),
			}),
		})
		assert.Equal(t, []schemas.ClassificationHint{{
			Kind: schemas.ClassificationHintFormattingOnly,
			Note: "all changed paths are whitespace-equivalent — skip Jev entirely (R10)",
		}}, hints)
	})

	t.Run("mixed changes, and generated+formatting mixes, follow strict precedence; empty → none", func(t *testing.T) {
		assert.Empty(t, analysis.DeriveClassificationHints(
			flagsFor([]string{"src/a.test.ts", "src/main.ts"})))
		assert.Equal(t, []schemas.ClassificationHint{{
			Kind: schemas.ClassificationHintGenerated,
			Note: "generated file(s) present in the diff — routing per policy: " +
				"inspect_generated_output (ARCH §8.8 memory pattern: generated-sdk)",
		}}, analysis.DeriveClassificationHints(flagsFor([]string{"dist/x.js", "src/b.min.js"})))
		assert.Empty(t, analysis.DeriveClassificationHints(nil))
	})
}

/* --------------------- V-P1-3: formatting-only sweep short-circuits (E2E)

The TypeScript case drove the V-P1-3 sweep through buildChangeAnalysis and
asserted the builder row + record-level hints (symbols_changed absent,
deep_analysis skipped, classification_hints). This port exercises the SAME
real-git sweep through the detectors: a whitespace-only edit between two real
commits, its real patch text read through the git layer, then the detector
flags and the derived classification hint. The builder-record assertions
(symbols_changed / deep_analysis / the record's classification_hints) belong
to the build unit's own tests (build.go is owned by another unit). */

// gitEnv mirrors the TS GIT_ENV determinism pins.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+00:00",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00+00:00",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}

// gitIdentity mirrors the TS IDENTITY commit-arg pins.
func gitIdentity() []string {
	return []string{"-c", "user.name=docsync-test", "-c", "user.email=docsync-test@invalid"}
}

func gitRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s failed: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

type sweepRepo struct{ root string }

func newSweepRepo(t *testing.T) *sweepRepo {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, nil, "init", "-b", "main")
	// Windows platform-port (DX-P11-3): repo-local line-ending policy — pin LF
	// in-repo before any file is written.
	gitRun(t, root, nil, "config", "core.autocrlf", "false")
	gitRun(t, root, nil, "config", "core.eol", "lf")
	return &sweepRepo{root: root}
}

func (r *sweepRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	target := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
	require.NoError(t, os.WriteFile(target, []byte(content), 0o644))
}

func (r *sweepRepo) commitAll(t *testing.T, message string) string {
	t.Helper()
	gitRun(t, r.root, nil, append(gitIdentity(), "add", "-A")...)
	gitRun(t, r.root, gitEnv(), append(gitIdentity(), "commit", "-q", "-m", message)...)
	return gitRun(t, r.root, gitEnv(), "rev-parse", "HEAD")
}

// TestVP1_3FormattingOnlySweepShortCircuit (P1-T03/T04; V-P1-3): a
// whitespace-only sweep short-circuits with zero decision-model budget — the
// detector verdict over the REAL git patch and the derived hint.
func TestVP1_3FormattingOnlySweepShortCircuit(t *testing.T) {
	repo := newSweepRepo(t)
	repo.write(t, "src/alpha.ts", "const alpha = 1;\nconst beta = 2;\n")
	base := repo.commitAll(t, "base")

	repo.write(t, "src/alpha.ts", "const alpha = 1;\nconst beta = 2;   \n") // whitespace-only
	head := repo.commitAll(t, "whitespace sweep")

	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: repo.root})
	require.NoError(t, err)
	records, err := client.DiffFiles(t.Context(), base, head)
	require.NoError(t, err)

	var patch string
	found := false
	for _, record := range records {
		if record.Path == "src/alpha.ts" {
			patch = record.Patch
			found = true
		}
	}
	require.True(t, found, "src/alpha.ts missing from the real diff")

	flags := analysis.DetectFileFlags(analysis.DetectorFile{Path: "src/alpha.ts", Patch: patch})
	// The whitespace sweep IS formatting-only — and NO symbol-level work
	// happened (zero decision-model budget was spent: the detectors never
	// spawn symbol work).
	assert.True(t, flags.FormattingOnly)
	assert.False(t, flags.TestOnly)
	assert.False(t, flags.Generated)
	assert.Equal(t, []schemas.ClassificationHint{{
		Kind: schemas.ClassificationHintFormattingOnly,
		Note: "all changed paths are whitespace-equivalent — skip Jev entirely (R10)",
	}}, analysis.DeriveClassificationHints([]analysis.DetectedFileFlags{flags}))
}

/* ------------------------------------------------- static purity audit */

// TestStaticImportAudit ports the TS static audit: every analysis module
// imports nothing from network/HTTP/model-access surfaces. In Go the module
// specifiers are import paths; the regex-level extraction is kept (raw text,
// like the TS audit) so a concurrently-written sibling file cannot make the
// audit itself fail to parse.
func TestStaticImportAudit(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue // the TS audit covered src/ files only, not test files
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	require.NotEmpty(t, files)

	// Forbidden module specifiers: network/HTTP/model-access surfaces.
	forbidden := regexp.MustCompile(`^(net|fetch|openrouter)(/|$)`)
	importBlock := regexp.MustCompile(`(?s)import\s*\((.*?)\)`)
	quotedSpecifier := regexp.MustCompile(`"([^"]+)"`)
	singleImport := regexp.MustCompile(`(?m)^\s*import\s+(?:[A-Za-z_][A-Za-z0-9_.]*\s+)?"([^"]+)"`)

	for _, name := range files {
		text, err := os.ReadFile(name)
		require.NoError(t, err)
		var specifiers []string
		for _, match := range importBlock.FindAllStringSubmatch(string(text), -1) {
			for _, specifier := range quotedSpecifier.FindAllStringSubmatch(match[1], -1) {
				specifiers = append(specifiers, specifier[1])
			}
		}
		for _, match := range singleImport.FindAllStringSubmatch(string(text), -1) {
			specifiers = append(specifiers, match[1])
		}
		for _, specifier := range specifiers {
			assert.False(t, forbidden.MatchString(specifier),
				"%s imports %q — analysis modules must not touch http/net/openrouter/fetch", name, specifier)
		}
	}

	// Audit sanity: this unit's ported modules are actually among the audited
	// files (the TS analog asserted detect.ts et al. are present).
	assert.Contains(t, files, "detect.go")
	assert.Contains(t, files, "generated.go")
}
