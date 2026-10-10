// Ported from packages/policy/src/index.ts (barrel) — Go has no barrels, so
// this file carries no re-exports and no exported symbols of its own; see
// doc.go for the package documentation.
//
// The TypeScript barrel was:
//
//	export const POLICY_PACKAGE_VERSION = '0.0.0' as const;
//	export * from './choice-sets.js';
//	export * from './schema.js';
//	export * from './question-composition.js';
//	export * from './compiler.js';
//	export * from './versioning.js';
//	export * from './bootstrap/policy-v1.js';
//	export * from './jev/runtime.js';
//	export * from './jev/answers.js';
//	export * from './decision/emit.js';
//	export * from './decision/abstain.js';
//	export * from './memo/jev-memo.js';
//	export * from './engine/matrix.js';
//	export * from './engine/outcome.js';
//	export * from './metrics/health.js';
//	export * from './calibration/harness.js';
//	export * from './calibration/thresholds.js';
//	export * from './calibration/binding.js';
//	export * from './orchestrator/index.js';
//	export * from './evaluation/index.js';
//	export * from './architect/index.js';
//	export * from './promotion/index.js';
//	export * from './governance/index.js';
//	export * from './shadow/index.js';
//
// In Go every module of the package declares its own exported identifiers and
// the import path IS the barrel, so nothing is re-exported here. The surface
// this unit ports (its own files, in the barrel's order):
//
//	choice-sets.go           — PolicyChoiceSetID, PolicyChoiceSets,
//	                           ClosedPolicyChoices, IsClosedPolicyChoice
//	                           (choice-sets.ts)
//	question-composition.go  — QuestionSource, CompositionConflict,
//	                           ComposeQuestions (question-composition.ts)
//	policy-v1.go             — BootstrapPolicyV1, BootstrapPolicyCompileError,
//	                           CompileBootstrapPolicyV1 (bootstrap/policy-v1.ts)
//	health.go                — HealthCounts, HealthMetrics, HealthBounds,
//	                           HealthEvaluation, RecordHealthMetrics,
//	                           EvaluateHealthBounds,
//	                           AbstainRateHealthDefaultBounds (metrics/health.ts)
//	gates.go                 — PromotionGateLibrary, PromotionGateInputs,
//	                           GateRow, PromotionGateResult, CompositeScore,
//	                           TwoProportionZ, MandatoryGateNames,
//	                           MinComparisonSizeDefault,
//	                           SignificanceAlphaDefault, ZCritical005
//	                           (evaluation/gates.ts)
//	evaluation-harness.go    — OfflineEvalHarness, ComparisonReport,
//	                           CaseEvaluations, HarnessCaseEvaluation
//	                           (evaluation/harness.ts)
//
// The remaining barrel entries (schema, compiler, versioning, Jev runtime,
// decision, memo, engine, calibration, orchestrator, architect, promotion,
// governance, shadow) are ported by other units of this package; each of their
// files documents its own surface, so this listing intentionally stops at the
// files this unit owns.

package policy
