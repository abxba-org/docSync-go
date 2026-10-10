// Package policy is the Go port of packages/policy — the repository-specific,
// self-calibrating documentation decision-policy engine (ARCH §8; the 14
// binding decisions D1–D14): the deterministic policy compiler, the decision
// engine and Jev runtime, calibration, the promotion pipeline with its gate
// library, governance (learning.mode resolution), and the Phase-12
// shadow/canary evaluation (ARCH §8.6; D6).
//
// Import constraint (plan.md §3, layer L4): this package may import ONLY
// internal/schemas, internal/docutil and internal/state — never a higher
// layer, and never the Go equivalents of @docsync/core (OD-1: the policy
// package must not import core; the Phase-10 split manifests and
// ChangeAnalysis records are consumed as DATA with their shapes pinned
// structurally). Fail closed everywhere: degrade to less automation, never
// more (D14).
//
// Ports landing in this package (each file cites its exact TS source):
//
//	packages/policy/src/shadow/matrix.ts  → matrix.go   (P12-T04; V-P12-3)
//	packages/policy/src/shadow/replay.ts  → replay.go   (P12-T01; V-P12-2)
//	packages/policy/src/shadow/live.ts    → live.go     (P12-T02; V-P12-1)
//
// plus the three matching TS test files (shadow-matrix.test.ts,
// shadow-replay.test.ts, shadow-live.test.ts) ported to matrix_test.go,
// replay_test.go and live_test.go.
package policy
