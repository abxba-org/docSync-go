// Package evaluation is the Go port of the Phase-10/Phase-12 evaluation module
// (P10-T07..T13 + T24, P12-T19; ARCH §9; CTX §35/§36) from
// packages/core/src/evaluation/:
//
//	dataset.go    core/src/evaluation/dataset.ts    — the historical-PR replay
//	                                                   dataset builder, its
//	                                                   fail-closed rejection
//	                                                   bookkeeping and the A3
//	                                                   dataset-yield report
//	metrics.go    core/src/evaluation/metrics.ts    — the six-metric
//	                                                   evaluation report
//	                                                   computation
//	                                                   (docsync.evaluation-report/v1),
//	                                                   the abstain-health
//	                                                   monitor and the
//	                                                   verifier-disagreement
//	                                                   metric (W1b-2b)
//	monitoring.go core/src/evaluation/monitoring.ts — the steady-state
//	                                                   monitoring surface:
//	                                                   per-(repository,
//	                                                   policy-version) rows,
//	                                                   spot-check audit rate,
//	                                                   abstain-health and
//	                                                   per-policy-version cost
//	                                                   attribution dashboards
//	                                                   (P12-T19)
//	objective.go  core/src/evaluation/objective.ts  — the CTX §36 improvement
//	                                                   objective scorer
//	splits.go     core/src/evaluation/splits.ts     — deterministic
//	                                                   seed-pinned frozen /
//	                                                   rolling splits with the
//	                                                   contamination check
//	spotcheck.go  core/src/evaluation/spotcheck.ts  — the seeded spot-check
//	                                                   sampler and
//	                                                   audit-finding ingestion
//	                                                   (OD-R25)
//
// The TS barrel (core/src/evaluation/index.ts) and its wiring into
// packages/core/src/index.ts are NOT ported here: barrels are orchestrator
// owned. Consumers import this package directly — exactly like the TS tests
// imported the module by relative path.
//
// Everything is data-in / data-out with injected identity and time: case and
// label ids are canonical ULIDs (docutil.ULIDGenerator — monotonic, never a
// wall-clock read inside the package), and `at` / `computed_at` / `created_at`
// / `now` are ALWAYS supplied by the caller (there is no `new Date()` anywhere
// in the TS module and no `time.Now()` in the logic under test). Splits and
// spot-check selection are driven by the caller's seeded PRNG (mulberry32) —
// `Math.random` appears nowhere in the TS module and no `math/rand` default
// source is used here.
//
// Fail-closed discipline (preserved everywhere): thresholds, recall floors,
// split fractions and sampler rates are exported versioned DATA (D11-adjacent
// — never silently hard-coded); a run record that cannot yield a schema-valid
// case is skipped AND counted (rejected[]); every computed report is
// re-validated against its registered schema before return; the
// unsafe-auto-updates gate collapses the improvement objective and any
// nonzero value makes a steady-state summary unhealthy (ARCH §8.4 / §8.6).
package evaluation
