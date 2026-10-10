// Package precedence ports packages/core/src/precedence: the precedence engine
// (P0-T07, ARCH §11.3). Layers, most restrictive wins, floors dominate —
// applied in this order:
//
//  5. built-in defaults: mode "suggest", learning "propose", report-only
//     (check-only) posture, abstain-on-uncertainty;
//  4. manifest `defaults:` block;
//  3. per-document manifest entry;
//  2. org floors — a violation is a VALIDATION ERROR (returned, never clamped);
//  1. invocation flags — TIGHTEN ONLY, applied LAST.
//
// Flags are additive tightening, modeled at the type level: InvocationFlags
// has no field that can loosen a mode or bypass a lock (there is deliberately
// no AllowAutoOverride — see that struct's comment). Flags only ever raise
// protection: dry-run and an explicit ReportOnly=true force the report-only
// posture ON (the built-in default is already ON — report-only is the V1
// posture per D10 / PLAN §0.1; only an explicit flags.reportOnly=false, the
// reserved slot for a future reviewed config, may drop it, and it can never
// change the MODE itself).
//
// D2 stays embedded: mode/ownership resolution is delegated to
// modes.ResolveEffectiveMode, so no downstream stage can bypass ownership
// floors.
//
// Export surface (mirrors the TS precedence/index.ts barrel):
//
//	resolve.go: InvocationFlags, PrecedenceInput, AppliedSource, EffectiveConfig,
//	  ResolveEffectiveConfig
package precedence
