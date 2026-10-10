// Package statemachine is the Go port of the docSync core statemachine module
// (P1-T17; ARCH §6.2): the doc lifecycle state machine over
// `state/docs/<path>.json` with its append-only transition log, the typed
// decision seam, CAS-guarded writes, and the deterministic zero-model SUSPECT
// driver.
//
// Ported files (traceability, plan.md §7):
//
//   - transitions.go — core/src/statemachine/transitions.ts (the legal-transition
//     table + ownership map; the single authority for WHICH transitions are
//     legal and WHO owns them).
//   - machine.go     — core/src/statemachine/machine.ts (DocStateMachine:
//     legality → ownership → append-only log → CAS, fail closed at each step;
//     IllegalTransitionError / DecisionRequiredError).
//   - suspect.go     — core/src/statemachine/suspect.ts (markSuspectsFromAnalysis:
//     the deterministic SUSPECT driver; zero model invocations, fail closed per
//     D14), composing the landed sibling packages: graph (orphaned-edge
//     detection + graph.SourceMatchesPath matching + GraphUnusableError) and
//     baselines (the invalidation engine + the D12 resolveBaseline ladder).
//   - statemachine_test.go — packages/core/test/statemachine.test.ts +
//     statemachine-suspect.test.ts.
//
// The package reuses internal/schemas (DocStateName values, DocState,
// DocStateTransition, Baseline, ChangeAnalysis), internal/state (StateStore
// DocState/Baseline APIs, StateRead, CasWriteResult), internal/graph, and
// internal/baselines without editing them.
package statemachine
