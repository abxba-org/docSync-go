package statemachine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// Ported from core/src/statemachine/suspect.ts (P1-T17 wiring; ARCH §6.2, D12;
// OD-R38 rulings).
//
// The Phase 1 structural half of the §6.2 table: for every graph-mapped
// document it combines, WITHOUT any model invocation (binding — zero in all of
// Phase 1),
//
//   - the §6.2 MAPPED-SOURCE/CLAIM intersection — the graph mapping's
//     declarative sources (same matching as the reverse index:
//     graph.SourceMatchesPath) plus the claim `source.file` intersection plus
//     the ChangeAnalysis symbol-change hook — computed INDEPENDENT of the
//     baseline (MAJOR-1 fix: a VERIFIED doc with mapped-source drift but no
//     baseline record still re-routes to SUSPECT);
//   - the D12 ladder for its baseline (baselines.ResolveBaseline: exact Sha →
//     tree → ancestor → patch-id → orphaned-suspect) plus the baseline's
//     resolved-literal orphan signals (verbatim baseline sources absent at the
//     pinned revision) WHEN a baseline exists;
//   - the graph's orphaned edges (graph.DetectOrphans: deleted/renamed literal
//     sources);
//
// and applies the deterministic transitions it owns (OD-R38): VERIFIED→SUSPECT,
// CLEAR→SUSPECT, and REJECTED→SUSPECT through the state machine. Reasons are
// ALWAYS computed and REPORTED for every mapped doc with a state record —
// the transition happens only where the §6.2 table makes the driver the edge's
// owner; every other state (SUSPECT, REVIEW, STALE, PROPOSED, CLOSED) is
// reported untransitioned with its evidence.
//
// R10 short-circuit skip (OD-R38): when the analysis record carries a
// whole-change SHORT-CIRCUIT classification hint (formatting_only or test_only
// — all changed files match; the generated hint does NOT skip, it routes per
// policy), the driver does NOT churn docs into SUSPECT: formatting/test-only
// sweeps must not feed the decision loop. Skipped docs report their
// invalidation reasons PLUS a short-circuit reason naming the hint and the R10
// decision-stage skip.
//
// Reason dedup: kind+detail first; then, for source-orphaned, a source-aware
// collapse — when a graph orphan edge AND a baseline orphan signal fire for the
// SAME vanished source, ONE reason survives (graph-edge wording preferred);
// distinct sources keep distinct reasons (MAJOR fix M).
//
// Determinism: graph mapping order, per-doc evidence deduplicated + sorted
// (baselines/invalidate.ts ordering), fixed ladder walk. CAS conflicts during
// the transition are reported per-doc (conflict: cas-conflict) and leave the
// winner's state intact — the run re-runs, nothing corrupts. A graph with
// configuration errors is refused outright (fail closed: a broken graph must
// never route documents).

// driverOwnedSuspectEdges are the §6.2 edges this driver owns (deterministic
// owner per OD-R38): new structural drift on a VERIFIED, CLEARED, or REJECTED
// doc re-routes it to SUSPECT. Every other state is report-only for this
// driver.
var driverOwnedSuspectEdges = []schemas.DocStateName{
	schemas.DocStateVerified,
	schemas.DocStateClear,
	schemas.DocStateRejected,
}

// shortCircuitHintKinds are the whole-change short-circuit hints that SKIP the
// transition (R10): the generated hint is deliberately absent — it routes per
// policy, per ARCH §8.8 (inspect_generated_output).
var shortCircuitHintKinds = []schemas.ClassificationHintKind{
	schemas.ClassificationHintFormattingOnly,
	schemas.ClassificationHintTestOnly,
}

func shortCircuitReason(hintKind schemas.ClassificationHintKind) baselines.InvalidationReason {
	return baselines.InvalidationReason{
		Kind: baselines.ReasonShortCircuit,
		Detail: fmt.Sprintf(
			"whole-change short-circuit hint \"%s\" — R10 decision-stage skip (no SUSPECT churn for formatting/test-only sweeps)",
			hintKind),
	}
}

// SuspectDriverStore is the store surface the driver needs: baseline reads +
// the doc-state machine backend (TS SuspectDriverStore = BaselineStoreBackend
// & DocStateMachineBackend; *state.StateStore satisfies it).
type SuspectDriverStore interface {
	DocStateMachineBackend
	ReadBaseline(docPath string) (state.StateRead[*schemas.Baseline], error)
}

// SuspectDriverInput is the driver request (TS SuspectDriverInput). Git is the
// concrete gitx client — exactly the TS structural GitClient (the D12 ladder
// consumes the same client; searchDepth nil is the 100 default, cap 500).
type SuspectDriverInput struct {
	Store         SuspectDriverStore
	Graph         graph.DocumentGraph
	Analysis      schemas.ChangeAnalysis
	Git           *gitx.GitClient
	Head          string
	RunID         string
	PolicyVersion int
	// At is the ISO-8601 timestamp of the transitions (caller-owned; no hidden clock).
	At     string
	Claims []baselines.InvalidationClaim
	// SearchDepth is the ladder walk depth (default 100, cap 500).
	SearchDepth *int
}

// SuspectReportEntry is the per-document outcome (graph mapping order; TS
// SuspectReportEntry). From is nil when the doc has no state record yet; To is
// SUSPECT when transitioned, else nil; LadderRun is set only when a baseline
// existed; Conflict is cas-conflict when the state write lost the CAS.
type SuspectReportEntry struct {
	Doc          string
	From         *schemas.DocStateName
	To           *schemas.DocStateName
	Transitioned bool
	Reasons      []baselines.InvalidationReason
	LadderRun    *baselines.LadderRun
	Conflict     string
}

// SuspectReport is the driver outcome (TS SuspectReport). ModelInvocations is
// the binding proof that the structural driver performs ZERO model
// invocations.
type SuspectReport struct {
	Entries           []SuspectReportEntry
	TransitionedCount int
	ModelInvocations  int
}

func graphOrphanReason(edge graph.OrphanEdge) baselines.InvalidationReason {
	if edge.OldPath == "" {
		return baselines.InvalidationReason{
			Kind:   baselines.ReasonSourceOrphaned,
			Detail: fmt.Sprintf(`graph source "%s" %s`, edge.Source, edge.Kind),
		}
	}
	return baselines.InvalidationReason{
		Kind:   baselines.ReasonSourceOrphaned,
		Detail: fmt.Sprintf(`graph source "%s" %s (from "%s")`, edge.Source, edge.Kind, edge.OldPath),
	}
}

// orphanReasons builds the orphan reasons for one doc with the MAJOR-fix M
// collapse: graph orphan edges first (preferred wording); a baseline
// resolved-literal signal is added only for vanished sources WITHOUT a graph
// edge. Key = the literal source; distinct sources keep distinct reasons.
func orphanReasons(
	edges []graph.OrphanEdge,
	baselineSources []string,
	changedFiles []baselines.InvalidationChangedFile,
	existsAtHead func(path string) bool,
) []baselines.InvalidationReason {
	bySource := make(map[string]baselines.InvalidationReason)
	for _, edge := range edges {
		if _, has := bySource[edge.Source]; !has {
			bySource[edge.Source] = graphOrphanReason(edge)
		}
	}
	if baselineSources != nil {
		deletedOrRenamed := make([]baselines.InvalidationChangedFile, 0, len(changedFiles))
		for _, file := range changedFiles {
			if file.Status == baselines.ChangeDeleted || file.Status == baselines.ChangeRenamed {
				deletedOrRenamed = append(deletedOrRenamed, file)
			}
		}
		for _, signal := range baselines.BaselineOrphanSignals(baselines.BaselineOrphanSignalsInput{
			Sources:      baselineSources,
			ChangedFiles: deletedOrRenamed,
			ExistsAtHead: existsAtHead,
		}) {
			if _, has := bySource[signal.Source]; !has {
				bySource[signal.Source] = signal.Reason
			}
		}
	}
	out := make([]baselines.InvalidationReason, 0, len(bySource))
	for _, reason := range bySource {
		out = append(out, reason)
	}
	return out
}

func summarizeReasons(reasons []baselines.InvalidationReason) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s: %s", reason.Kind, reason.Detail))
	}
	return strings.Join(parts, "; ")
}

func dedupeReasons(reasons []baselines.InvalidationReason) []baselines.InvalidationReason {
	byKey := make(map[string]baselines.InvalidationReason, len(reasons))
	for _, reason := range reasons {
		key := string(reason.Kind) + "\x00" + reason.Detail
		if _, has := byKey[key]; !has {
			byKey[key] = reason
		}
	}
	out := make([]baselines.InvalidationReason, 0, len(byKey))
	for _, reason := range byKey {
		out = append(out, reason)
	}
	// baselines/invalidate.ts ordering: kind, then detail (byte-wise).
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Detail < b.Detail
	})
	return out
}

// toInvalidationChangedFiles maps ChangeAnalysis files onto the invalidation
// engine's shape.
func toInvalidationChangedFiles(files []schemas.ChangedFile) []baselines.InvalidationChangedFile {
	out := make([]baselines.InvalidationChangedFile, 0, len(files))
	for _, file := range files {
		out = append(out, baselines.InvalidationChangedFile{
			Path:    file.Path,
			Status:  baselines.InvalidationChangeStatus(file.Status),
			OldPath: file.OldPath,
		})
	}
	return out
}

// orphanChangedFiles maps the deleted/renamed ChangeAnalysis files onto the
// orphan-detection shape (TS deletedOrRenamed; graph.OrphanChangedFile carries
// OldPath as a plain string — "" means absent).
func orphanChangedFiles(files []schemas.ChangedFile) []graph.OrphanChangedFile {
	out := make([]graph.OrphanChangedFile, 0, len(files))
	for _, file := range files {
		if file.Status != schemas.FileStatusDeleted && file.Status != schemas.FileStatusRenamed {
			continue
		}
		entry := graph.OrphanChangedFile{Path: file.Path, Status: string(file.Status)}
		if file.OldPath != nil {
			entry.OldPath = *file.OldPath
		}
		out = append(out, entry)
	}
	return out
}

// MarkSuspectsFromAnalysis is the deterministic SUSPECT driver (ported from
// core/src/statemachine/suspect.ts): ZERO model invocations, fail closed (a
// graph with configuration errors is refused outright; every git failure
// propagates).
func MarkSuspectsFromAnalysis(ctx context.Context, input SuspectDriverInput) (SuspectReport, error) {
	if len(input.Graph.Errors) > 0 {
		return SuspectReport{}, graph.NewGraphUnusableError(input.Graph.Errors)
	}

	machine := NewDocStateMachine(input.Store)
	changedFiles := toInvalidationChangedFiles(input.Analysis.Files)
	deletedOrRenamed := orphanChangedFiles(input.Analysis.Files)
	headEntries, err := input.Git.LsTree(ctx, input.Head, nil)
	if err != nil {
		return SuspectReport{}, err
	}
	headFiles := make(map[string]bool, len(headEntries))
	for _, entry := range headEntries {
		headFiles[entry.Path] = true
	}
	existsAtHead := func(path string) bool { return headFiles[path] }
	orphanEdges := graph.DetectOrphans(graph.DetectOrphansInput{
		ChangedFiles: deletedOrRenamed,
		Graph:        input.Graph,
		ExistsAtHead: existsAtHead,
	})
	orphansByDoc := make(map[string][]graph.OrphanEdge)
	for _, edge := range orphanEdges {
		orphansByDoc[edge.Doc] = append(orphansByDoc[edge.Doc], edge)
	}
	sourceSymbolsChanged := false
	for i := range input.Analysis.Files {
		if input.Analysis.Files[i].SymbolsChanged != nil && *input.Analysis.Files[i].SymbolsChanged {
			sourceSymbolsChanged = true
			break
		}
	}

	// R10 short-circuit (OD-R38): one whole-change hint suppresses EVERY
	// transition this run; the generated hint does not skip (routes per
	// policy).
	var shortCircuitHint *schemas.ClassificationHintKind
	for i := range input.Analysis.ClassificationHints {
		hint := input.Analysis.ClassificationHints[i]
		for _, kind := range shortCircuitHintKinds {
			if hint.Kind == kind {
				matched := hint.Kind
				shortCircuitHint = &matched
				break
			}
		}
		if shortCircuitHint != nil {
			break
		}
	}

	entries := make([]SuspectReportEntry, 0, len(input.Graph.Mappings))
	for _, mapping := range input.Graph.Mappings {
		doc := mapping.Doc
		current, err := machine.GetState(doc)
		if err != nil {
			return SuspectReport{}, err
		}
		if current == nil {
			entries = append(entries, SuspectReportEntry{Doc: doc, Reasons: []baselines.InvalidationReason{}})
			continue
		}

		// Evidence is ALWAYS computed and reported (ARCH §6.2 trigger: ANY of
		// mapped sources / claims / baseline intersects) — the transition is
		// the only owner-gated step. (1) Mapped-source/claim intersection,
		// independent of the baseline.
		intersection, err := baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
			Sources:              mapping.Sources,
			ChangedFiles:         changedFiles,
			Claims:               input.Claims,
			SourceSymbolsChanged: sourceSymbolsChanged,
		})
		if err != nil {
			return SuspectReport{}, err
		}
		reasons := append([]baselines.InvalidationReason{}, intersection.Reasons...)

		// (2) Baseline identity: the D12 ladder run + the baseline's own
		// intersection and resolved-literal orphan signals, when present.
		baseline, err := input.Store.ReadBaseline(doc)
		if err != nil {
			return SuspectReport{}, err
		}
		var ladderRun *baselines.LadderRun
		var baselineSources []string
		if baseline.Exists && baseline.Content != nil {
			// D12 (MAJOR-2 fix): the ladder is INCONCLUSIVE-RESOLVING, never
			// clock-based — R15 is the driver's question; the identity ladder
			// failure routes history-rewritten baselines to SUSPECT.
			ladder, err := baselines.ResolveBaseline(ctx, baselines.ResolveBaselineInput{
				Baseline:    baseline.Content,
				Head:        input.Head,
				Git:         input.Git,
				SearchDepth: input.SearchDepth,
			})
			if err != nil {
				return SuspectReport{}, err
			}
			run := ladder.Run
			ladderRun = &run
			if !ladder.BaselineValid {
				reasons = append(reasons, baselines.InvalidationReason{
					Kind: baselines.ReasonHistoryRewrite,
					Detail: fmt.Sprintf("baseline identity unresolved at %s: %s",
						input.Head, ladder.Evidence),
				})
			}
			baselineIntersection, err := baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
				Sources:      baseline.Content.SourceFiles,
				ChangedFiles: changedFiles,
			})
			if err != nil {
				return SuspectReport{}, err
			}
			reasons = append(reasons, baselineIntersection.Reasons...)
			baselineSources = baseline.Content.SourceFiles
		}

		// (3) Orphan evidence: graph edges + baseline signals, collapsed per
		// vanished source (graph wording preferred).
		reasons = append(reasons, orphanReasons(orphansByDoc[doc], baselineSources, changedFiles, existsAtHead)...)

		deduped := dedupeReasons(reasons)
		canTransition := false
		for _, edge := range driverOwnedSuspectEdges {
			if edge == current.State {
				canTransition = true
				break
			}
		}
		from := current.State
		if len(deduped) == 0 {
			entries = append(entries, SuspectReportEntry{
				Doc:       doc,
				From:      &from,
				Reasons:   deduped,
				LadderRun: ladderRun,
			})
			continue
		}
		// R10 short-circuit skip: evidence exists, but a formatting/test-only
		// sweep must not churn the doc into SUSPECT — report with a
		// short-circuit reason appended after the evidence.
		if canTransition && shortCircuitHint != nil {
			withShortCircuit := append(append([]baselines.InvalidationReason{}, deduped...),
				shortCircuitReason(*shortCircuitHint))
			entries = append(entries, SuspectReportEntry{
				Doc:       doc,
				From:      &from,
				Reasons:   withShortCircuit,
				LadderRun: ladderRun,
			})
			continue
		}
		if canTransition {
			write, err := machine.ApplyTransition(ApplyTransitionInput{
				DocPath:       doc,
				To:            schemas.DocStateSuspect,
				RunID:         input.RunID,
				Reason:        summarizeReasons(deduped),
				PolicyVersion: input.PolicyVersion,
				At:            input.At,
				EvidenceRefs:  []string{input.Analysis.RunID},
			})
			if err != nil {
				return SuspectReport{}, err
			}
			if !write.OK {
				entries = append(entries, SuspectReportEntry{
					Doc:       doc,
					From:      &from,
					Reasons:   deduped,
					LadderRun: ladderRun,
					Conflict:  StateMutationKindCasConflict,
				})
				continue
			}
			to := schemas.DocStateSuspect
			entries = append(entries, SuspectReportEntry{
				Doc:          doc,
				From:         &from,
				To:           &to,
				Transitioned: true,
				Reasons:      deduped,
				LadderRun:    ladderRun,
			})
			continue
		}
		// Legal-but-not-owned states (SUSPECT, REVIEW, STALE, PROPOSED, CLOSED):
		// reported with the computed evidence, never re-transitioned here.
		entries = append(entries, SuspectReportEntry{
			Doc:       doc,
			From:      &from,
			Reasons:   deduped,
			LadderRun: ladderRun,
		})
	}

	transitionedCount := 0
	for _, entry := range entries {
		if entry.Transitioned {
			transitionedCount++
		}
	}
	return SuspectReport{
		Entries:           entries,
		TransitionedCount: transitionedCount,
		ModelInvocations:  0,
	}, nil
}
