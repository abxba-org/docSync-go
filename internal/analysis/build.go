// Ported from packages/core/src/analysis/build.ts (P1-T02; ARCH §4.1) with
// deep-analysis caps and downgrade (P1-T08; ARCH §3.7) and inconclusive-outcome
// semantics (P1-T18; D14, V-P1-10 groundwork).
//
// Deterministic orchestration order (same inputs → byte-identical record):
// resolveRev(base/head) → diffFiles + diffStat → cap assessment → composed
// analyzers (deepAllowed = !capBreached) → all-unavailable check → per-file
// merge (diff-authoritative rows, OR-merged plugin findings, pure detectors,
// semantic differs with content fetched per-file from pinned revs) →
// deep_analysis verdict → classification hints → strict schema validation
// (fail closed on drift).
//
// P1-T18 outcome semantics (D14: analyzer failure is inconclusive, never
// negative):
//   - a typed *gitx.GitExecError at ANY stage → `inconclusive`
//     (`git failure: <kind> — <stderrExcerpt>`) — NEVER an empty/zero-docs
//     record;
//   - every analyzer unavailable (no coverage entry with status 'ok') while
//     ≥1 file changed → `inconclusive` — analyzer failure is NOT "no docs
//     affected"; an empty diff with dead analyzers stays a valid empty record
//     (a genuine no-change fact);
//   - empty diff (0 files) → VALID analysis record with empty `files`;
//   - schema drift or a bad plugin lineup returns an ERROR (programming/config
//     errors fail loud — they are not environmental inconclusiveness).
//
// Purity: this file performs git I/O ONLY through the injected *gitx.GitClient
// (typed, fail-closed); all per-file judgment comes from the pure detectors
// (detect.go), pure differs (semantics.go), and pure plugins (plugin.go), so a
// completed record is cacheable by blob SHAs.

package analysis

import (
	"context"
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// AnalysisCaps is the deep-analysis budget caps (P1-T08): exceeding EITHER
// downgrades the run.
type AnalysisCaps struct {
	// MaxFiles is the maximum number of changed files analyzed with deep
	// analysis allowed.
	MaxFiles int
	// MaxDiffSize is the maximum diff-size budget, accounted as
	// Σ(additions+deletions) for text files (numstat counts) and the raw
	// patch-section byte length for binary files (git's textual binary stub —
	// a deterministic size proxy for a given path pair). The name says SIZE,
	// not bytes on the wire: text and binary files are accounted by different
	// units and the comparison is `byteSum > maxDiffSize`.
	MaxDiffSize int
}

// BuildChangeAnalysisInput is the builder input. RepoRoot is descriptive
// context; Git owns the actual root.
type BuildChangeAnalysisInput struct {
	// Repo is the repository identity used in `run_id` (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root (must match the git client's
	// root).
	RepoRoot string
	// Git is the typed read-only git client (executor-injectable for failure
	// tests).
	Git *gitx.GitClient
	// Base is the base ref or sha (resolved internally to a full commit sha).
	Base string
	// Head is the head ref or sha (resolved internally to a full commit sha).
	Head string
	// Plugins are the analyzer plugins (OD-4 composition; must be a valid
	// complete lineup).
	Plugins []AnalyzerPlugin
	// Caps are the deep-analysis caps; nil = unlimited (fixture/CLI scale).
	Caps *AnalysisCaps
	// FetchContentForGeneratedHints fetches head blob content for
	// generated-content markers (first 10 lines) for files the cheap path
	// check alone does NOT classify as generated. Deleted files are never
	// fetched. Off by default: one `cat-file` per candidate file.
	FetchContentForGeneratedHints bool
}

// AnalysisOutcomeKind is the discriminated-union tag of AnalysisOutcome
// (TS `kind`).
type AnalysisOutcomeKind string

// Analysis outcome kinds in TS union order.
const (
	AnalysisOutcomeKindAnalysis     AnalysisOutcomeKind = "analysis"
	AnalysisOutcomeKindInconclusive AnalysisOutcomeKind = "inconclusive"
)

// AnalysisOutcome is the P1-T18 outcome: either a schema-valid ChangeAnalysis
// record, or an INCONCLUSIVE verdict with the reason and whatever analyzer
// coverage was gathered before the failure (empty when git failed before
// analysis).
type AnalysisOutcome struct {
	Kind AnalysisOutcomeKind
	// Record is the analysis record when Kind is AnalysisOutcomeKindAnalysis.
	Record *schemas.ChangeAnalysis
	// Reason is the inconclusive reason when Kind is
	// AnalysisOutcomeKindInconclusive.
	Reason string
	// Coverage is the analyzer coverage gathered before an inconclusive
	// verdict (empty when git failed before analysis).
	Coverage []schemas.AnalyzerCoverage
}

// The fixed verdict texts (TS module constants).
const (
	deepSkippedDefaultReason         = "no type-aware analyzer coverage in V1 (typescript stub; tree-sitter lands Phase 11)"
	deepSkippedNoSymbolChangesReason = "symbol-capable analyzer ran but reported no symbol-level changes for this diff"
	capHintNote                      = "deep_analysis skipped (cap breach) — route REVIEW"
)

// capAssessment is the cap-accounting verdict (TS CapAssessment).
type capAssessment struct {
	breached  bool
	fileCount int
	byteSum   int
	// reason is the exact skipped-reason text ("" when not breached).
	reason string
}

// assessCaps is the cap accounting (P1-T08): text files count
// additions+deletions (numstat); binary files count their patch-section length
// as a deterministic size proxy (git's textual binary stub is stable for a
// given path pair).
func assessCaps(
	diffFiles []gitx.DiffFileRecord,
	diffStat []gitx.DiffStatRecord,
	caps *AnalysisCaps,
) capAssessment {
	statByPath := make(map[string]gitx.DiffStatRecord, len(diffStat))
	for _, record := range diffStat {
		statByPath[record.Path] = record
	}
	byteSum := 0
	for _, file := range diffFiles {
		stat, ok := statByPath[file.Path]
		if ok && !stat.Binary {
			additions := 0
			if stat.Additions != nil {
				additions = *stat.Additions
			}
			deletions := 0
			if stat.Deletions != nil {
				deletions = *stat.Deletions
			}
			byteSum += additions + deletions
		} else {
			byteSum += len(file.Patch)
		}
	}
	fileCount := len(diffFiles)
	breached := caps != nil && (fileCount > caps.MaxFiles || byteSum > caps.MaxDiffSize)
	reason := ""
	if breached {
		reason = fmt.Sprintf("cap breach (%d files / %d bytes > caps)", fileCount, byteSum)
	}
	return capAssessment{breached: breached, fileCount: fileCount, byteSum: byteSum, reason: reason}
}

// toAnalyzerInputFile maps one diff record onto the plugin input shape.
func toAnalyzerInputFile(file gitx.DiffFileRecord) AnalyzerInputFile {
	input := AnalyzerInputFile{
		Path:   file.Path,
		Status: schemas.FileStatus(file.Status),
		Patch:  strPtr(file.Patch),
	}
	if file.OldPath != nil {
		input.OldPath = file.OldPath
	}
	return input
}

// toAnalyzerInputFiles maps the whole diff onto the plugin input shape.
func toAnalyzerInputFiles(diffFiles []gitx.DiffFileRecord) []AnalyzerInputFile {
	out := make([]AnalyzerInputFile, 0, len(diffFiles))
	for _, file := range diffFiles {
		out = append(out, toAnalyzerInputFile(file))
	}
	return out
}

// toPathOnlyFiles maps the diff onto the minimal path-only input shape the
// semantic differs consume (TS `ReadonlyArray<{ path: string }>`).
func toPathOnlyFiles(diffFiles []gitx.DiffFileRecord) []PathOnlyFile {
	out := make([]PathOnlyFile, 0, len(diffFiles))
	for _, file := range diffFiles {
		out = append(out, PathOnlyFile{Path: file.Path})
	}
	return out
}

// assessDeepAnalysis is the deep-analysis verdict (P1-T08): cap breach wins;
// otherwise `performed` iff any plugin finding reports symbols_changed;
// otherwise skipped with the fixed V1 reason.
func assessDeepAnalysis(
	findings []PerFileFinding,
	capBreached bool,
	capReason string,
	coverage []schemas.AnalyzerCoverage,
) schemas.DeepAnalysis {
	if capBreached {
		reason := capReason
		return schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: &reason}
	}
	for _, finding := range findings {
		if finding.SymbolsChanged {
			return schemas.DeepAnalysis{Status: schemas.DeepAnalysisPerformed}
		}
	}
	// Declared reason honesty (P11-T05/E2): when the symbol-capable TypeScript
	// analyzer actually ran ('ok'), a skip is NOT a stub gap — it is a real
	// "no symbol changes" verdict; the stale stub reason would be a false
	// declaration in the run record.
	typescriptRan := false
	for _, entry := range coverage {
		if entry.Analyzer == schemas.AnalyzerNameTypeScript && entry.Status == schemas.AnalyzerStatusOK {
			typescriptRan = true
			break
		}
	}
	reason := deepSkippedDefaultReason
	if typescriptRan {
		reason = deepSkippedNoSymbolChangesReason
	}
	return schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: &reason}
}

// mergedRows is the per-file merge result (TS MergedRows).
type mergedRows struct {
	files []schemas.ChangedFile
	flags []DetectedFileFlags
}

// mergeRows is the per-file merge (deterministic git diff order):
// diff-authoritative row + OR-merged plugin findings + pure detectors +
// semantic differs. Content fetches are per-file and sequential (`cat-file` at
// pinned revs): generated hints fetch head content ONLY for non-deleted files
// the path check alone did not classify; config differs fetch base+head
// content ONLY for modified files in the config family. Config-family differs
// are assessed ONLY for `status === 'modified'` config files — added/renamed
// config files are left UNASSESSED (the `config_diff` boolean stays absent),
// which is the fail-closed, documented representation for a semantic compare
// the V1 differ cannot perform.
//
// Row-boolean representation (strict v1 schema): detector flags are always
// assessed, so they are written ONLY when true (absent = false); assessment
// booleans from plugins/differs are written whenever assessed (absent =
// not assessed; false = assessed and unchanged; true = assessed and changed).
func mergeRows(
	ctx context.Context,
	git *gitx.GitClient,
	baseSha string,
	headSha string,
	diffFiles []gitx.DiffFileRecord,
	findings []PerFileFinding,
	fetchContentForGeneratedHints bool,
) (mergedRows, error) {
	// TS `new Map(findings.map(...))`: a later duplicate path wins.
	findingByPath := make(map[string]PerFileFinding, len(findings))
	for _, finding := range findings {
		findingByPath[finding.Path] = finding
	}
	files := make([]schemas.ChangedFile, 0, len(diffFiles))
	flags := make([]DetectedFileFlags, 0, len(diffFiles))

	for _, file := range diffFiles {
		pathOnlyGenerated := IsGeneratedFile(DetectorFile{Path: file.Path})
		var contentAtHead *string
		if fetchContentForGeneratedHints && !pathOnlyGenerated && file.Status != gitx.DiffStatusDeleted {
			content, err := git.CatFile(ctx, headSha, file.Path)
			if err != nil {
				return mergedRows{}, err
			}
			contentAtHead = strPtr(content.Content)
		}
		flag := DetectFileFlags(DetectorFile{Path: file.Path, Patch: file.Patch, ContentAtHead: contentAtHead})
		flags = append(flags, flag)

		finding, hasFinding := findingByPath[file.Path]
		row := schemas.ChangedFile{Path: file.Path, Status: schemas.FileStatus(file.Status)}
		if file.OldPath != nil {
			row.OldPath = file.OldPath
		}
		if flag.Generated {
			row.Generated = boolPtr(true)
		}
		if flag.TestOnly {
			row.TestOnly = boolPtr(true)
		}
		if flag.FormattingOnly {
			row.FormattingOnly = boolPtr(true)
		}
		if hasFinding {
			symbolsChanged := finding.SymbolsChanged
			row.SymbolsChanged = &symbolsChanged
			// P11-T03/E2: the structured symbol evidence rides the row
			// unchanged (already schema-shaped — the analyzer projects it at
			// emission).
			if finding.SymbolsDetail != nil {
				row.SymbolsDetail = finding.SymbolsDetail
			}
		}

		// API contract signal (P1-T05): openapi family → api_diff true. The
		// ApiDiffResult note is function-level evidence — the strict v1 schema
		// has no per-file note field, so it is not recordable here.
		api := ApiDiffFor(PathOnlyFile{Path: file.Path})
		if hasFinding || api != nil {
			apiDiff := false
			if hasFinding {
				apiDiff = finding.APIDiff
			}
			row.APIDiff = boolPtr(apiDiff || api != nil)
		}

		// Config semantic diff (P1-T05): ONLY modified files in the config
		// family (budget: at most two cat-file calls per such file).
		configAssessed := hasFinding
		configDiff := false
		if hasFinding {
			configDiff = finding.ConfigDiff
		}
		if file.Status == gitx.DiffStatusModified && ClassifyPathFamily(file.Path) == PathFamilyConfig {
			oldContent, err := git.CatFile(ctx, baseSha, file.Path)
			if err != nil {
				return mergedRows{}, err
			}
			newContent, err := git.CatFile(ctx, headSha, file.Path)
			if err != nil {
				return mergedRows{}, err
			}
			config := ConfigDiffFor(file.Path, oldContent.Content, newContent.Content)
			if config != nil && config.ConfigDiff != nil {
				if *config.ConfigDiff {
					configAssessed = true
					configDiff = true
				} else if !configAssessed {
					configAssessed = true
					configDiff = false
				}
				// config_diff nil (declared gap): never downgrades an existing
				// assessment to false — the row keeps whatever was already
				// assessed.
			}
		}
		if configAssessed {
			row.ConfigDiff = boolPtr(configDiff)
		}

		files = append(files, row)
	}
	return mergedRows{files: files, flags: flags}, nil
}

// anyCoverageOK reports whether any coverage entry declares status 'ok'
// (TS `coverage.some((entry) => entry.status === 'ok')`).
func anyCoverageOK(coverage []schemas.AnalyzerCoverage) bool {
	for _, entry := range coverage {
		if entry.Status == schemas.AnalyzerStatusOK {
			return true
		}
	}
	return false
}

// schemaDriftError renders a validation failure as a loud coded error
// (TS: CHANGE_ANALYSIS_SCHEMA.parse throws a ZodError out of the builder —
// fail closed on drift, never coerced into an inconclusive verdict).
func schemaDriftError(issues []schemas.Issue) error {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		parts = append(parts, fmt.Sprintf("%s: %s", strings.Join(issue.Path, "."), issue.Message))
	}
	return docutil.New(docutil.CodeSchema,
		"CHANGE_ANALYSIS_SCHEMA validation failed: "+strings.Join(parts, "; "))
}

// BuildChangeAnalysis builds a deterministic, schema-valid ChangeAnalysis
// draft for `(repo, base, head)` — or an inconclusive verdict when the
// environment fails (git) or the analyzers are all unavailable with changed
// files (D14/P1-T18).
func BuildChangeAnalysis(ctx context.Context, input BuildChangeAnalysisInput) (AnalysisOutcome, error) {
	// Compose FIRST: a bad plugin lineup is a programming error and must fail
	// loud, never masquerade as an environmental inconclusive.
	composed, err := ComposeAnalyzers(input.Plugins, nil)
	if err != nil {
		return AnalysisOutcome{}, err
	}
	coverage := []schemas.AnalyzerCoverage{}

	outcome, err := func() (AnalysisOutcome, error) {
		// ARCH §4.1 run_id contract — the run_id is keyed by the RESOLVED full
		// SHAs (the same values written into base_sha/head_sha), never the raw
		// input refs, so §3.5/§6.3 keying by run_id always agrees with the
		// record's own SHAs (a symbolic ref or short sha resolves identically).
		baseSha, err := input.Git.ResolveRev(ctx, input.Base)
		if err != nil {
			return AnalysisOutcome{}, err
		}
		headSha, err := input.Git.ResolveRev(ctx, input.Head)
		if err != nil {
			return AnalysisOutcome{}, err
		}
		runID := fmt.Sprintf("%s#%s..%s", input.Repo, baseSha, headSha)
		diffFiles, err := input.Git.DiffFiles(ctx, baseSha, headSha)
		if err != nil {
			return AnalysisOutcome{}, err
		}
		diffStat, err := input.Git.DiffStat(ctx, baseSha, headSha)
		if err != nil {
			return AnalysisOutcome{}, err
		}

		caps := assessCaps(diffFiles, diffStat, input.Caps)

		var notes []string
		if caps.breached {
			notes = []string{caps.reason}
		}
		analyzerRun := composed.RunAnalyzers(AnalyzerInput{
			Files:       toAnalyzerInputFiles(diffFiles),
			DeepAllowed: !caps.breached,
			Notes:       notes,
		})
		coverage = analyzerRun.Coverage

		// D14/P1-T18: with changed files, dead analyzers are INCONCLUSIVE —
		// never "no docs affected". (An empty diff stays a valid empty record.)
		if !anyCoverageOK(coverage) && len(diffFiles) > 0 {
			return AnalysisOutcome{
				Kind: AnalysisOutcomeKindInconclusive,
				Reason: fmt.Sprintf(
					"all analyzers unavailable with %d changed file(s) — analyzer failure is inconclusive, never negative (D14)",
					len(diffFiles)),
				Coverage: coverage,
			}, nil
		}

		merged, err := mergeRows(
			ctx,
			input.Git,
			baseSha,
			headSha,
			diffFiles,
			analyzerRun.Files,
			input.FetchContentForGeneratedHints,
		)
		if err != nil {
			return AnalysisOutcome{}, err
		}

		deepAnalysis := assessDeepAnalysis(analyzerRun.Files, caps.breached, caps.reason, coverage)
		hints := DeriveClassificationHints(merged.flags)
		if caps.breached {
			hints = append(hints, schemas.ClassificationHint{Kind: schemas.ClassificationHintOther, Note: capHintNote})
		}

		record := &schemas.ChangeAnalysis{
			Schema:              schemas.ChangeAnalysisSchemaID,
			RunID:               runID,
			Repo:                input.Repo,
			BaseSHA:             baseSha,
			HeadSHA:             headSha,
			Files:               merged.files,
			DependenciesChanged: DependencyChanged(toPathOnlyFiles(diffFiles)),
			ClassificationHints: hints,
			DeepAnalysis:        deepAnalysis,
			AnalyzerCoverage:    coverage,
		}
		if issues := record.Validate(); len(issues) > 0 {
			return AnalysisOutcome{}, schemaDriftError(issues)
		}
		return AnalysisOutcome{Kind: AnalysisOutcomeKindAnalysis, Record: record}, nil
	}()
	if err != nil {
		if gitErr, ok := gitx.AsGitExecError(err); ok {
			return AnalysisOutcome{
				Kind:     AnalysisOutcomeKindInconclusive,
				Reason:   fmt.Sprintf("git failure: %s — %s", gitErr.Kind, gitErr.StderrExcerpt),
				Coverage: coverage,
			}, nil
		}
		return AnalysisOutcome{}, err // schema drift / plugin lineup bugs fail loud (never coerced)
	}
	return outcome, nil
}

/* --------------------------------------------------------------- utilities */

// boolPtr returns a pointer to b, for optional boolean fields
// (TS `field: true` written only when true; absent = false).
func boolPtr(b bool) *bool { return &b }

// strPtr returns a pointer to s, for optional string fields.
func strPtr(s string) *string { return &s }
