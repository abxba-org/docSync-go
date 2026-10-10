// Package orchestrator ports the docSync Loop-A orchestrator
// (packages/core/src/orchestrator) to Go: the DETERMINISTIC stage graph
// (ARCH §3.3/§3.5/§4/§13; PLAN §6.4) plus the frozen RunOutcome API
// (V-P5-16).
//
// The orchestrator is a deterministic stage machine, not an agent: production
// orchestration is deterministic code and every LLM is a BOUNDED LEAF stage
// with schema-validated I/O (ARCH §13). The application — never the model —
// decides what is permitted (D11). Core-internal stages (analyze, map,
// invalidate, retrieval) are direct calls into the Phase-1 engines
// (internal/analysis, internal/graph, internal/statemachine,
// internal/retrieval, internal/baselines, internal/state); the AI stages
// (jev, writer, verification, policy) run through the STRUCTURAL PORT BUNDLE
// declared in types.go. Fail-closed discipline throughout (D8/D14): config
// errors surface typed (exit 2) before any AI stage; analyzer failure is a
// DECLARED `analyzer-inconclusive` degradation, never "no docs affected";
// every §3.3 payload received from an untrusted port is schema-validated at
// the edge (contracts.go) before use, and a violation degrades to LESS
// automation (REVIEW routing, abstain, named degradation) — never a crash and
// never a hand-patch.
//
// PORTS (the injectable seams; PipelinePorts in types.go):
//
//	DecisionStagePort     — JEV Stage-1 routing leaf (ARCH §7.1 three-way
//	                        branch: CONTINUE / REVIEW / IGNORE).
//	WriterStagePort       — Sonnet patch-writer leaf (ARCH §4.7); bounded
//	                        in-process pool (OD-R10/P5-T12), results applied
//	                        in candidate order, never completion order.
//	VerificationStagePort — three-tier verifier leaf (judge-independent).
//	PolicyStagePort       — versioned policy engine leaf; maps a
//	                        (decision, verification) pair onto the outcome.
//	EarlySignalPort       — optional early-signal sink (post once after the
//	                        decision, final update after persist).
//
// Every port is an interface with typed input/output structs, so tests inject
// fakes keyed by request identity with no LLM and no network
// (run_test.go / injection_test.go port orchestrator-e2e.test.ts and
// orchestrator-injection.test.ts against them).
//
// SEAMS (declared here; rebound by the composition layer at wave end):
//
//   - internal/policy (landed) exposes `CreateJevDecisionStage` /
//     `CreatePolicyStagePort` plus the structural bridges
//     `NewBridgeCoreDecisionStage` (input: `{analysis}`) and
//     `PolicyStagePort.Resolve` (input `{Ctx{Manifest}, DocPath, Decision,
//     Verification}`). Their method shapes are NOT nominally identical to the
//     interfaces below (Go interfaces are nominal), so the wave-end adapter
//     translates core-shaped inputs onto those bridges and maps the results
//     back onto DecisionStageResult / PolicyStageResult. This package must
//     NOT import internal/policy (OD-1: core depends on schemas + Phase-1
//     engines only).
//   - internal/openrouter (landed) exposes the writer stage as
//     `WriterStagePort.Write(WriterStageInput)` with its own mirrored context
//     shapes — the same adapter pattern (TODO(orchestrator, wave-end): bind
//     the seam).
//   - internal/verifier (NOT yet landed when this package was written): the
//     VerificationStagePort interface below is the declared seam; the
//     concrete three-tier verifier adapter binds at wave end
//     (TODO(verifier): no dependency from this package — only the interface).
//   - internal/gitpr (Phase 6 doc-PR write path) is NOT consumed by this
//     package: the Loop-A orchestrator persists run records and stops at the
//     policy stage. The Phase-6 composition layer owns the gitpr binding; no
//     seam is declared here.
//
// Exit-code contract (ARCH §12.1; exit-codes.go): 0 no drift · 1 drift ·
// 2 configuration error (fail-closed BEFORE any AI stage; D8) · 3 runtime
// failure (fail-closed; D14). Report/check-render/check-conclusion derive the
// CLI and GitHub surfaces from ONE canonical report (report.go) so local and
// CI never disagree about the facts.
package orchestrator
