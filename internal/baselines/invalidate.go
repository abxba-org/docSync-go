package baselines

import (
	"fmt"
	"sort"

	"github.com/DUDE616/docSync-go/internal/globs"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Ported from core/src/baselines/invalidate.ts: the baseline invalidation
// engine (P1-T15; ARCH §6.1). PURE: no git, no fs, no clocks — the caller
// injects the changed-file set (from a ChangeAnalysis record), optional claim
// shapes, and the optional symbol-change signal. Comparison is by
// PATH/CONTENT identity of the recorded baseline, never by timestamps: a
// baseline is invalidated when the diff between `baseline_commit` and head
// touches what the document was verified against.
//
// Rules (each contributes typed reasons; the union is returned):
//   - changed path matches any `baseline.source_files` (literal equality or
//     glob, via sourceMatchesPath) → SourceChanged;
//   - a deleted/renamed change matching a LITERAL source → SourceOrphaned
//     (glob sources pin nothing and are never orphaned — same rule as the
//     graph's orphan detection), plus, when ExistsAtHead is provided, any
//     literal source absent at the pinned revision;
//   - changed path matching a claim's `source.file` → ClaimSourceChanged
//     (the matching claim ids are named in the detail);
//   - SourceSymbolsChanged === true → SymbolChange — the documented Phase 11
//     hook: V1 has NO symbol data in baselines, so the driver forwards the
//     ChangeAnalysis `symbols_changed` signal as a boolean and this module
//     records it without pretending to know which symbols moved.
//
// The engine is composed of two exported stages so callers can intersect
// ARBITRARY source lists (the SUSPECT driver intersects the graph mapping's
// declarative sources INDEPENDENT of the baseline, ARCH §6.2):
//   - EvaluateSourceIntersection — path intersection over an explicit source
//     list (SourceChanged), the claim intersection (ClaimSourceChanged), and
//     the symbol-change hook; NO orphan signals;
//   - BaselineOrphanSignals — the literal-source orphan rules over an explicit
//     source list, returned STRUCTURED (source + reason) so the driver can
//     deduplicate graph orphan edges and baseline signals for the same
//     vanished source (prefer the graph wording).
//
// EvaluateInvalidation = intersection over `baseline.source_files` + its orphan
// signals — identical reasons to running both stages.
//
// `ShortCircuit` is a driver-side reason kind (not produced here): the SUSPECT
// driver reports it when a whole-change R10 short-circuit hint suppresses the
// transition despite invalidation evidence.
//
// Deterministic: reasons are deduplicated (kind+detail) and sorted by kind,
// then detail (byte-wise). `Invalidated` is exactly len(Reasons) > 0.

// InvalidationReasonKind is the typed invalidation signal vocabulary.
// `HistoryRewrite` is produced by the D12 ladder layer (no git here) and
// `ShortCircuit` by the SUSPECT driver.
type InvalidationReasonKind string

// The invalidation reason kinds.
const (
	ReasonSourceChanged      InvalidationReasonKind = "source-changed"
	ReasonSourceOrphaned     InvalidationReasonKind = "source-orphaned"
	ReasonClaimSourceChanged InvalidationReasonKind = "claim-source-changed"
	ReasonHistoryRewrite     InvalidationReasonKind = "history-rewrite"
	ReasonSymbolChange       InvalidationReasonKind = "symbol-change"
	ReasonShortCircuit       InvalidationReasonKind = "short-circuit"
)

// InvalidationReason is one typed invalidation signal.
type InvalidationReason struct {
	Kind   InvalidationReasonKind
	Detail string
}

// InvalidationChangeStatus is the changed-file status vocabulary (ChangeAnalysis
// `files` rows).
type InvalidationChangeStatus string

// The changed-file statuses.
const (
	ChangeAdded    InvalidationChangeStatus = "added"
	ChangeModified InvalidationChangeStatus = "modified"
	ChangeDeleted  InvalidationChangeStatus = "deleted"
	ChangeRenamed  InvalidationChangeStatus = "renamed"
)

// InvalidationChangedFile is the changed-file shape this engine consumes.
type InvalidationChangedFile struct {
	Path   string
	Status InvalidationChangeStatus
	// OldPath is the pre-change path for renames (nil otherwise).
	OldPath *string
}

// InvalidationClaim is the minimal claim shape consumed for claim-source
// intersection (a schemas.Claim satisfies it structurally).
type InvalidationClaim struct {
	ID     string
	Source InvalidationClaimSource
}

// InvalidationClaimSource is the claim's source file reference.
type InvalidationClaimSource struct {
	File string
}

// EvaluateInvalidationInput is the input of EvaluateInvalidation.
type EvaluateInvalidationInput struct {
	Baseline *schemas.Baseline
	// ChangedFiles is the changed-file set for this run.
	ChangedFiles []InvalidationChangedFile
	// ExistsAtHead is the optional pinned-revision existence check for the
	// orphan fallback rule.
	ExistsAtHead func(path string) bool
	// Claims is the optional claim shapes intersected by `source.file`.
	Claims []InvalidationClaim
	// SourceSymbolsChanged is the ChangeAnalysis symbol signal forwarded as a
	// boolean (Phase 11 hook).
	SourceSymbolsChanged bool
}

// EvaluateSourceIntersectionInput is the input of EvaluateSourceIntersection.
type EvaluateSourceIntersectionInput struct {
	// Sources is an explicit source list (verbatim globs or literal paths).
	Sources []string
	// ChangedFiles is the changed-file set for this run.
	ChangedFiles []InvalidationChangedFile
	// Claims is the optional claim shapes intersected by `source.file`.
	Claims []InvalidationClaim
	// SourceSymbolsChanged is the ChangeAnalysis symbol signal (Phase 11 hook).
	SourceSymbolsChanged bool
}

// BaselineOrphanSignalsInput is the input of BaselineOrphanSignals.
type BaselineOrphanSignalsInput struct {
	// Sources is an explicit source list (verbatim globs or literal paths).
	Sources []string
	// ChangedFiles: only deleted/renamed changes can orphan a literal source.
	ChangedFiles []InvalidationChangedFile
	// ExistsAtHead is the optional pinned-revision existence check for the
	// fallback rule.
	ExistsAtHead func(path string) bool
}

// SourceOrphanSignal is one structured literal-source orphan signal: the
// vanished source path (the dedupe/collapse key across graph edges and
// baseline signals) plus its typed reason.
type SourceOrphanSignal struct {
	// Source is the literal source entry that no longer resolves.
	Source string
	Reason InvalidationReason
}

// InvalidationResult is the engine outcome: `Invalidated` is exactly
// len(Reasons) > 0.
type InvalidationResult struct {
	Invalidated bool
	Reasons     []InvalidationReason
}

// globMetaChars is the glob-significant charset recognized by the docSync glob
// engine (TS GLOB_META_CHARS): `*`, `?`, `[...]`, `{...}`, `!` and extglobs
// `(...)`. Conservative by design — when in doubt a pattern is a glob (glob
// sources pin nothing, so they never orphan).
const globMetaChars = "*?[]{}!()"

// isLiteralSourcePattern ports graph/lookup.ts's isLiteralSourcePattern: true
// when the pattern contains no glob metacharacters (a pinned literal path).
func isLiteralSourcePattern(pattern string) bool {
	return len(pattern) > 0 && !containsAnyRune(pattern, globMetaChars)
}

// containsAnyRune reports whether s contains any char of chars.
func containsAnyRune(s, chars string) bool {
	for _, c := range s {
		for _, m := range chars {
			if c == m {
				return true
			}
		}
	}
	return false
}

// sourceMatchesPath ports graph/lookup.ts's sourceMatchesPath: a literal source
// matches by exact path equality (picomatch is never consulted for it); a glob
// source is compiled through the shared glob engine, whose ARCH §11.3 escape
// guards fail closed (the GlobGuardError is propagated to the caller, never
// silently downgraded to "no match").
func sourceMatchesPath(source, changedPath string) (bool, error) {
	if isLiteralSourcePattern(source) {
		return source == changedPath, nil
	}
	glob, err := globs.CompileGlob(source)
	if err != nil {
		return false, err
	}
	return glob.Match(changedPath), nil
}

// statusSuffix ports the TS statusSuffix: the ` (status)` / `
// (status from "old")` trailer appended to every path-bearing detail.
func statusSuffix(file InvalidationChangedFile) string {
	if file.OldPath == nil {
		return fmt.Sprintf(" (%s)", file.Status)
	}
	return fmt.Sprintf(" (%s from %q)", file.Status, *file.OldPath)
}

// baselineSourceFiles is the nil-safe accessor for the baseline's recorded
// source list.
func baselineSourceFiles(baseline *schemas.Baseline) []string {
	if baseline == nil {
		return nil
	}
	return baseline.SourceFiles
}

// dedupeAndSortReasonKeys renders the TS dedupe key (kind + NUL + detail).
func dedupeAndSortReasonKeys(reasons []InvalidationReason) []InvalidationReason {
	byKey := make(map[string]InvalidationReason, len(reasons))
	for _, reason := range reasons {
		byKey[string(reason.Kind)+"\x00"+reason.Detail] = reason
	}
	out := make([]InvalidationReason, 0, len(byKey))
	for _, reason := range byKey {
		out = append(out, reason)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Detail < out[j].Detail
	})
	return out
}

// EvaluateSourceIntersection is the path intersection stage (NO orphan rules):
// changed paths matching any of the given sources (ReasonSourceChanged), claims
// referencing a changed path (ReasonClaimSourceChanged), and the symbol-change
// hook. Pure and deterministic; used by both EvaluateInvalidation (baseline
// sources) and the SUSPECT driver (graph mapping sources — independent of the
// baseline).
func EvaluateSourceIntersection(input EvaluateSourceIntersectionInput) (InvalidationResult, error) {
	var reasons []InvalidationReason
	add := func(kind InvalidationReasonKind, detail string) {
		reasons = append(reasons, InvalidationReason{Kind: kind, Detail: detail})
	}

	for _, file := range input.ChangedFiles {
		for _, source := range input.Sources {
			// A change is relevant when it touches a path the source matches —
			// either the post-change path or, for renames, the pre-change path
			// (a glob source whose match moved away still changed the doc's world).
			matchedPath, err := sourceMatchesPath(source, file.Path)
			if err != nil {
				return InvalidationResult{}, err
			}
			if !matchedPath && file.OldPath != nil {
				matchedPath, err = sourceMatchesPath(source, *file.OldPath)
				if err != nil {
					return InvalidationResult{}, err
				}
			}
			if matchedPath {
				add(ReasonSourceChanged, fmt.Sprintf("source %q matches changed file %q%s",
					source, file.Path, statusSuffix(file)))
			}
		}
		matchingClaims := make([]string, 0, len(input.Claims))
		for _, claim := range input.Claims {
			if claim.Source.File == file.Path {
				matchingClaims = append(matchingClaims, claim.ID)
			}
		}
		if len(matchingClaims) > 0 {
			sort.Strings(matchingClaims)
			add(ReasonClaimSourceChanged, fmt.Sprintf("claims [%s] reference changed file %q%s",
				joinStrings(matchingClaims, ", "), file.Path, statusSuffix(file)))
		}
	}

	if input.SourceSymbolsChanged {
		add(ReasonSymbolChange,
			"source symbols changed (V1 records no symbol data — ChangeAnalysis signal; Phase 11 hook)")
	}

	reasons = dedupeAndSortReasonKeys(reasons)
	return InvalidationResult{Invalidated: len(reasons) > 0, Reasons: reasons}, nil
}

// BaselineOrphanSignals is the literal-source orphan rule set over an explicit
// source list (glob sources pin nothing and are NEVER orphaned — same rule as
// the graph's orphan detection). STRUCTURED output: each signal carries its
// vanished source so callers can collapse duplicate signals (graph edge +
// baseline signal for the same deletion) by source, preferring the graph
// wording.
func BaselineOrphanSignals(input BaselineOrphanSignalsInput) []SourceOrphanSignal {
	var signals []SourceOrphanSignal
	for _, source := range input.Sources {
		if !isLiteralSourcePattern(source) {
			continue
		}
		orphanChange := findOrphanChange(input.ChangedFiles, source)
		if orphanChange != nil {
			detail := fmt.Sprintf("source %q deleted", source)
			if orphanChange.Status == ChangeRenamed {
				detail = fmt.Sprintf("source %q renamed away to %q", source, orphanChange.Path)
			}
			signals = append(signals, SourceOrphanSignal{
				Source: source,
				Reason: InvalidationReason{Kind: ReasonSourceOrphaned, Detail: detail},
			})
			continue
		}
		if input.ExistsAtHead != nil && !input.ExistsAtHead(source) {
			signals = append(signals, SourceOrphanSignal{
				Source: source,
				Reason: InvalidationReason{
					Kind:   ReasonSourceOrphaned,
					Detail: fmt.Sprintf("source %q does not exist at the pinned revision", source),
				},
			})
		}
	}
	return signals
}

// findOrphanChange returns the first deleted/renamed change that vanished the
// literal source (the TS `changedFiles.find`).
func findOrphanChange(files []InvalidationChangedFile, source string) *InvalidationChangedFile {
	for i := range files {
		file := files[i]
		if (file.Status == ChangeDeleted && file.Path == source) ||
			(file.Status == ChangeRenamed && file.OldPath != nil && *file.OldPath == source) {
			return &files[i]
		}
	}
	return nil
}

// EvaluateInvalidation is the composed engine: the intersection over
// `baseline.source_files` plus the baseline's own orphan signals — identical
// reasons to running both stages.
func EvaluateInvalidation(input EvaluateInvalidationInput) (InvalidationResult, error) {
	intersection, err := EvaluateSourceIntersection(EvaluateSourceIntersectionInput{
		Sources:              baselineSourceFiles(input.Baseline),
		ChangedFiles:         input.ChangedFiles,
		Claims:               input.Claims,
		SourceSymbolsChanged: input.SourceSymbolsChanged,
	})
	if err != nil {
		return InvalidationResult{}, err
	}
	orphanReasons := make([]InvalidationReason, 0, len(intersection.Reasons))
	for _, signal := range BaselineOrphanSignals(BaselineOrphanSignalsInput{
		Sources:      baselineSourceFiles(input.Baseline),
		ChangedFiles: input.ChangedFiles,
		ExistsAtHead: input.ExistsAtHead,
	}) {
		orphanReasons = append(orphanReasons, signal.Reason)
	}
	reasons := dedupeAndSortReasonKeys(append(append([]InvalidationReason{}, intersection.Reasons...), orphanReasons...))
	return InvalidationResult{Invalidated: len(reasons) > 0, Reasons: reasons}, nil
}

// joinStrings is the TS `Array.join` used for the claim-id detail list.
func joinStrings(values []string, sep string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += sep
		}
		out += value
	}
	return out
}
