// Package modes ports packages/core/src/modes: effective-mode resolution
// (P0-T05, ARCH §11.2) and org floors (P0-T06, P12-T13; ARCH §11.2/§11.3/§11.4).
//
// Binding decisions embedded here and nowhere else:
//
//   - D2 — ownership is the floor: manifest modes/ownership override ANY
//     classifier confidence. `locked` resolves `blocked` at any confidence and
//     Jev can never override ownership. `ClassifierConfidence` is accepted as
//     an input solely so this module can demonstrably IGNORE it (V-P0-5): the
//     resolution is deep-equal for confidence 0.01 and 0.99.
//   - `locked` is TERMINAL: no flag or policy layer converts it; the only path
//     to a mode change is a human PR editing manifest.yml.
//   - `generated`/`external` docs are EXCLUDED from patch candidates: drift is
//     observed and reported, the AI never fights another generator.
//   - `suggest`/`human` docs ARE patch candidates (ARCH §11.2: the writer
//     produces a proposal that ships through a doc PR for human review) but are
//     never auto-applied; only `auto` may auto-apply, and the precedence layer
//     (internal/precedence) additionally gates that behind the report-only
//     posture.
//
// Export surface (mirrors the TS modes/index.ts barrel):
//
//	resolve.go: ModeLadder, IsMorePermissive, OwnershipCategories,
//	  OwnershipCategory, ModeResolutionInput, ModeResolution,
//	  ResolveEffectiveMode, IsDocMode
//	floors.go:  ModeCeilingFloor, ReportOnlyUntilReviewedFloor, OrgFloor,
//	  IsModeCeilingFloor, IsReportOnlyFloor, FloorViolation,
//	  FloorCheckAdvisory, FloorCheckResult, FloorCheckParams,
//	  AssertValidFloorPattern, FloorMatchesPath, FloorSummary,
//	  ComposedFloorMax, CheckFloors, EnforceFloors
//	errors.go:  EDOCSYNC_FLOOR, EDOCSYNC_MODE_CONFLICT, ModeFloorViolation,
//	  ModeFloorViolationError, ModeConflictError
package modes
