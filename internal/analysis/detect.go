package analysis

// Ported from packages/core/src/analysis/detect.ts (P1-T03/P1-T04;
// ARCH §3.7/§8.8; R10).
//
// Three per-file detectors over {path, patch, contentAtHead} — all PURE (no
// fs/network/git/clock, deterministic for identical input):
//   - IsTestOnlyFile: path matches test-path conventions.
//   - IsFormattingOnlyFile: every removed/added hunk-line pair is
//     whitespace-equivalent (trimmed equality) — pure-mode-only and
//     rename-with-no-hunks diffs are NOT formatting (nothing textually paired).
//   - IsGeneratedFile: generated-path conventions OR generated-content markers
//     in the first 10 lines (content checked ONLY when the caller provides
//     contentAtHead; the path check always runs first because it is cheap).
//
// DeriveClassificationHints folds per-file flags into change-level hints with
// FIXED deterministic precedence (mutually exclusive short-circuits):
//  1. `generated`   — iff ANY changed file is generated. This is NOT a
//     skip-all signal: generated files route per policy through the stable
//     InspectGeneratedOutputAction action (`inspect_generated_output`,
//     ARCH §8.8 memory pattern `generated-sdk`).
//  2. `test_only`       — iff ALL changed files are test-only (and none
//     generated): the decision stage is SKIPPED entirely — zero decision-model
//     budget (V-P1-3).
//  3. `formatting_only` — iff ALL changed files are formatting-only (and none
//     generated): decision stage SKIPPED (V-P1-3).
//
// Path-pattern interpretation (documented, conservative): name-convention
// patterns (`*.test.*`, `*.spec.*`) match at ANY depth (doublestar-prefixed);
// directory-convention patterns (`test/**`, `tests/**`, `spec/**`, `e2e/**`)
// are read literally as TOP-LEVEL directories, and `__tests__/**` matches at
// any depth (the only location convention that exists nested in real trees).
// Under-matching is deliberate (fail closed): a missed test path only wastes
// decision budget, while over-matching could wrongly suppress review.

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// InspectGeneratedOutputAction is the stable policy action for generated-file
// routing (ARCH §8.8 `generated-sdk`; TS INSPECT_GENERATED_OUTPUT_ACTION).
const InspectGeneratedOutputAction = "inspect_generated_output"

// DetectorFile is the minimal detector input (structural — build rows and
// tests pass subsets; TS DetectorFile).
type DetectorFile struct {
	Path string
	// Patch is unified patch text when available; formatting detection needs
	// it. The TS `patch?: string` treats undefined and "" identically
	// (IsFormattingOnlyFile is false for both), so one field carries both.
	Patch string
	// ContentAtHead is file content at HEAD when the builder chose to fetch
	// it (generated markers). The TS `contentAtHead?: string` treats undefined
	// and "" identically (IsGeneratedFile skips the content check for both),
	// so nil and a pointer to "" behave the same; nil also lets the builder
	// express "not fetched" without a sentinel.
	ContentAtHead *string
}

// DetectedFileFlags are the per-file detector verdicts (all three always
// assessed — absent never means unknown; TS DetectedFileFlags).
type DetectedFileFlags struct {
	Path           string `json:"path"`
	TestOnly       bool   `json:"test_only"`
	FormattingOnly bool   `json:"formatting_only"`
	Generated      bool   `json:"generated"`
}

/* --------------------------------------------------------------- test-only */

// testPathPatterns are the test-path conventions (TS TEST_PATH_PATTERNS),
// matched with picomatch's { dot: true } semantics through the shared glob
// engine (internal/globs).
var testPathPatterns = []string{
	"**/*.test.*",
	"**/*.spec.*",
	"**/__tests__/**",
	"test/**",
	"tests/**",
	"spec/**",
	"e2e/**",
}

var testPathMatchers = mustCompileGlobs(testPathPatterns)

// IsTestOnlyFile reports whether path matches the test-path conventions.
//
// Seam: the signature takes the path string (the Go idiom for a detector that
// reads only `.path`); ClassifyPathFamily in semantics.go reuses this exact
// function for its 'test' family check — one implementation, never duplicated.
func IsTestOnlyFile(path string) bool {
	return matchesAny(testPathMatchers, path)
}

/* ---------------------------------------------------------- formatting-only */

// binaryPatchMarkers: a patch containing either sentinel is a binary diff —
// never formatting (TS BINARY_PATCH_MARKERS).
var binaryPatchMarkers = []string{"Binary files ", "GIT binary patch"}

// IsFormattingOnlyFile reports whether the patch is a text diff whose every
// removed/added line pair (in hunk order) is equal after trimming.
// Line-count mismatches, content changes, added/deleted files, renames
// without hunks, and mode-only changes are all NOT formatting-only.
func IsFormattingOnlyFile(file DetectorFile) bool {
	patch := file.Patch
	if len(patch) == 0 {
		return false
	}
	for _, marker := range binaryPatchMarkers {
		if strings.Contains(patch, marker) {
			return false
		}
	}

	removed := []string{}
	added := []string{}
	sawHunk := false
	sawLineChange := false

	currentHunkIsFormattingOnly := func() bool {
		if len(removed) != len(added) {
			return false
		}
		for index := 0; index < len(removed); index++ {
			if strings.TrimSpace(removed[index]) != strings.TrimSpace(added[index]) {
				return false
			}
		}
		return true
	}

	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			// Hunk boundary: evaluate the hunk that just closed (fail fast).
			if !currentHunkIsFormattingOnly() {
				return false
			}
			sawHunk = true
			removed = []string{}
			added = []string{}
			continue
		}
		// Only lines INSIDE a hunk body are content: before the first `@@` the
		// `+`/`-` prefixes belong to `+++`/`---`/rename headers.
		if !sawHunk {
			continue
		}
		if strings.HasPrefix(line, "-") {
			removed = append(removed, line[1:])
			sawLineChange = true
		} else if strings.HasPrefix(line, "+") {
			added = append(added, line[1:])
			sawLineChange = true
		}
		// Context (' '), metadata ('\ No newline ...'), and empty lines: ignored.
	}
	if !sawHunk || !sawLineChange {
		return false // rename/mode-only/no-op: not formatting
	}
	return currentHunkIsFormattingOnly()
}

/* ------------------------------------------------------------- generated */

// generatedPathPatterns are the generated-path conventions
// (TS GENERATED_PATH_PATTERNS).
var generatedPathPatterns = []string{
	"**/generated/**",
	"**/__generated__/**",
	"**/*.gen.ts",
	"**/*.gen.js",
	"**/dist/**",
	"**/build/**",
	"**/*.min.js",
}

var generatedPathMatchers = mustCompileGlobs(generatedPathPatterns)

// generatedContentMarkers are the marker substrings searched in the FIRST 10
// lines of file content only (TS GENERATED_CONTENT_MARKERS).
var generatedContentMarkers = []string{
	"@generated",
	"DO NOT EDIT",
	"do not edit",
	"Automatically generated",
}

// generatedContentWindowLines is the content-marker window (TS
// GENERATED_CONTENT_WINDOW_LINES).
const generatedContentWindowLines = 10

// IsGeneratedFile reports whether the file is generated by path convention or
// by generated-content markers in the first 10 lines. The path check always
// runs first because it is cheap; content participates only when the caller
// populated ContentAtHead.
func IsGeneratedFile(file DetectorFile) bool {
	if matchesAny(generatedPathMatchers, file.Path) {
		return true
	}
	content := file.ContentAtHead
	if content == nil || len(*content) == 0 {
		return false
	}
	headLines := strings.Split(*content, "\n")
	if len(headLines) > generatedContentWindowLines {
		headLines = headLines[:generatedContentWindowLines]
	}
	for _, line := range headLines {
		for _, marker := range generatedContentMarkers {
			if strings.Contains(line, marker) {
				return true
			}
		}
	}
	return false
}

/* ----------------------------------------------------------------- folding */

// DetectFileFlags runs all three detectors for one file. The path-based
// generated check is cheap and always runs; content markers participate only
// when the CALLER populated contentAtHead (the builder fetches content only
// for files the path check alone did not already classify as generated).
func DetectFileFlags(file DetectorFile) DetectedFileFlags {
	return DetectedFileFlags{
		Path:           file.Path,
		TestOnly:       IsTestOnlyFile(file.Path),
		FormattingOnly: IsFormattingOnlyFile(file),
		Generated:      IsGeneratedFile(file),
	}
}

/* ------------------------------------------------------------------ hints */

var (
	// generatedHintNote is the `generated` hint note (TS GENERATED_HINT_NOTE).
	generatedHintNote = "generated file(s) present in the diff — routing per policy: " +
		InspectGeneratedOutputAction + " (ARCH §8.8 memory pattern: generated-sdk)"
	// testOnlyHintNote is the `test_only` hint note (TS TEST_ONLY_HINT_NOTE).
	testOnlyHintNote = "all changed paths match test patterns — skip Jev entirely (R10)"
	// formattingOnlyHintNote is the `formatting_only` hint note (TS
	// FORMATTING_ONLY_HINT_NOTE).
	formattingOnlyHintNote = "all changed paths are whitespace-equivalent — skip Jev entirely (R10)"
)

// DeriveClassificationHints returns the change-level classification hints with
// FIXED deterministic precedence — at most ONE hint is emitted (a
// short-circuit means the decision stage is SKIPPED, zero decision-model
// budget, V-P1-3; `generated` instead routes through
// InspectGeneratedOutputAction):
//  1. any file generated      → `generated` hint;
//  2. all files test_only     → `test_only` hint;
//  3. all files formatting_only → `formatting_only` hint;
//
// otherwise no hint (mixed change → the decision stage decides).
func DeriveClassificationHints(flags []DetectedFileFlags) []schemas.ClassificationHint {
	if len(flags) == 0 {
		return []schemas.ClassificationHint{}
	}
	for _, flag := range flags {
		if flag.Generated {
			return []schemas.ClassificationHint{{
				Kind: schemas.ClassificationHintGenerated,
				Note: generatedHintNote,
			}}
		}
	}
	every := func(pred func(DetectedFileFlags) bool) bool {
		for _, flag := range flags {
			if !pred(flag) {
				return false
			}
		}
		return true
	}
	if every(func(flag DetectedFileFlags) bool { return flag.TestOnly }) {
		return []schemas.ClassificationHint{{
			Kind: schemas.ClassificationHintTestOnly,
			Note: testOnlyHintNote,
		}}
	}
	if every(func(flag DetectedFileFlags) bool { return flag.FormattingOnly }) {
		return []schemas.ClassificationHint{{
			Kind: schemas.ClassificationHintFormattingOnly,
			Note: formattingOnlyHintNote,
		}}
	}
	return []schemas.ClassificationHint{}
}

/* --------------------------------------------------------------- utilities */

// mustCompileGlobs compiles the static pattern sets once, the Go equivalent of
// the TS module-level `picomatch(PATTERNS, { dot: true })` matcher. The
// patterns are compile-time constants vetted by the detector tests; a
// rejection is a programming error surfaced immediately at init, never a
// silent runtime "no match".
func mustCompileGlobs(patterns []string) []*globs.CompiledGlob {
	out := make([]*globs.CompiledGlob, 0, len(patterns))
	for _, pattern := range patterns {
		glob, err := globs.CompileGlob(pattern)
		if err != nil {
			panic(docutil.New(docutil.CodeInternal,
				fmt.Sprintf("analysis: static glob pattern %q rejected: %v", pattern, err)))
		}
		out = append(out, glob)
	}
	return out
}

// matchesAny reports whether path matches any compiled pattern — the picomatch
// array-matcher semantics (an array of non-negated patterns matches iff ANY
// matches).
func matchesAny(matchers []*globs.CompiledGlob, path string) bool {
	for _, matcher := range matchers {
		if matcher.Match(path) {
			return true
		}
	}
	return false
}
