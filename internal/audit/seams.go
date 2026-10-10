// Unlanded-dependency seams (CONVENTIONS.md: "when in doubt: fail closed" —
// an unlanded package is declared as a seam, never imported and never
// duplicated wholesale).
//
// The Loop B audit package's own logic is complete (types/dedup/incremental/
// scheduler/artifacts/store/compose/org-report). Two shapes it hands to, or
// receives from, packages that are NOT landed in this repo yet are declared
// here as minimal, documented seams:
//
//   - `CheckSummaryPayload` — the Phase-8 orchestrator check-run payload
//     (core/src/orchestrator/check-conclusion.ts). internal/orchestrator is
//     not landed; the audit renderers CONSTRUCT this payload and hand it back
//     as DATA (nothing is posted here), so the shape is declared locally and
//     carries the TS field names. When internal/orchestrator lands, replace
//     this declaration with a type alias to its payload (one-line change).
//   - `PipelineInput` / `PipelinePorts` / `LoopAPipelineEntry` — the Loop-A
//     pipeline surface (core/src/orchestrator/{types,run}.ts) the drift
//     SUGGEST path funnels through (P11-T21, V-P11-10 "one code path, two
//     triggers"). The flow only needs the manifest allowlist (the D7 gate)
//     and passes every other field through VERBATIM to the injected entry, so
//     the ports bundle stays opaque and the entry is a caller-injected
//     function. internal/evaluation is likewise not landed — the audit
//     package does not import it (no evaluation APIs are consumed by any
//     ported module here; the evaluation-gated surfaces live in the
//     orchestrator/policy layers above this one).
//
// No behavior is ported into these declarations: they are shapes only, and
// every consumer of them fails closed on absence (a nil entry invokes
// nothing).
package audit

import (
	"context"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// CheckSummaryPayload is the Phase-8 check-run summary payload triple
// (core/src/orchestrator/check-conclusion.ts `CheckSummaryPayload`:
// title/summary/text). The audit renderers produce it as DATA — the
// conclusion mapping and the posting stay caller-owned policy decisions
// (D10 advisory default), not renderer ones.
type CheckSummaryPayload struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// PipelineInput is the Loop-A pipeline input seam (core/src/orchestrator/
// types.ts `PipelineInput`): only the fields the drift SUGGEST flow itself
// reads (the manifest allowlist for the D7 gate) plus the pass-through fields
// the shared entry consumes. The optional TS fields (pr, caps, budgets,
// analyzers) are omitted here — the caller wires the real orchestrator input
// once that package lands, and this seam is then replaced by it.
type PipelineInput struct {
	// Repo is the repository identity (e.g. `org/name`).
	Repo string
	// RepoRoot is the absolute repository root.
	RepoRoot string
	// Base is the base ref/sha of the change window.
	Base string
	// Head is the head ref/sha of the change window.
	Head string
	// Manifest is the manifest allowlist for the run (D7) — the only field
	// the drift flow itself inspects.
	Manifest *schemas.Manifest
	// Policy is the versioned policy payload (opaque here; consumed by the
	// policy stage inside the shared pipeline).
	Policy any
	// Clock is the injected clock returning the current ISO-8601 instant.
	Clock func() string
}

// PipelinePorts is the Loop-A ports bundle (decision/writer/verification/
// policy stages). The drift SUGGEST path never inspects it — it hands the
// caller-composed bundle to the injected entry VERBATIM, so the shared
// policy stage inside decides every outcome — hence the opaque seam.
type PipelinePorts = any

// LoopAPipelineEntry is the shared Loop-A pipeline entry seam
// (core/src/orchestrator/run.ts `runPipeline`): the exact function object the
// PR trigger composes through. The drift path funnels through the SAME entry
// (V-P11-10); the flow receives it injected, so the identity requirement is
// satisfied by the caller wiring one instance, and asserted at the wiring
// site (`LoopAPipelineEntry === orchestrator.RunPipeline`) once
// internal/orchestrator lands.
type LoopAPipelineEntry func(ctx context.Context, input *PipelineInput, ports PipelinePorts) (*schemas.RunOutcome, error)
