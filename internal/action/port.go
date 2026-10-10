// Ported from packages/action/src/port.ts (P8-T16/T17): the DEFAULT CheckRunPort
// wiring — binds the runner's injected CheckRunPort seam to the composed
// pipeline runtime + the check-run REST surface.
//
// SEAM (internal/cli): the pipeline composition (`createCliPipelineRuntime` +
// run → canonical report) lives in `@docsync/cli`, ported to internal/cli,
// which is being written CONCURRENTLY. Per the migration seam discipline
// (CONVENTIONS.md §Behavior preservation; plan.md §3 import DAG), this package
// DECLARES the runtime seam below and does NOT import internal/cli (the import
// DAG forbids it) nor wire a concrete factory. The concrete composition — the
// config gate (internal/validate.ValidateRepositoryConfig, zero AI), the
// INV-8 model-env strip, the report-only writer stub, the canonical report
// build (internal/orchestrator.BuildCanonicalReport), the outage-signature
// classification, and the check-run REST upsert over internal/gitpr (403 →
// logged skip, A7/L9) — is bound by the cli-composition wave into this seam.
//
// Until that lands, RunMain runs with an injected Check (all action tests) or,
// with neither, FAILS CLOSED to the config-error lane rather than silently
// reporting a check with no pipeline behind it (D8/D14).
//
// The seam shape is the faithful contract the future binding must satisfy:
//
//	PipelineRuntimeFactory.Create(PipelineRuntimeOptions) → PipelineRuntime
//	PipelineRuntime.Run(PipelineRunInput{repo,base,head,pr}) → *schemas.RunOutcome

package action

import (
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// PipelineRunInput is the per-invocation run input the CLI runtime consumes
// (mirrors CliRuntimeRunInput's base/head/pr the port forwards).
type PipelineRunInput struct {
	// Repo is the repository run-key identity (owner/name).
	Repo string
	// Base is the base ref of the change window.
	Base string
	// Head is the head commit SHA.
	Head string
	// PRNumber is present on pull_request events only.
	PRNumber *int
}

// PipelineRuntime is the composed pipeline runtime seam (the ported
// `createCliPipelineRuntime(...).run`). Run composes/runs the deterministic
// stage machine and returns the schema-validated RunOutcome. Implementation:
// internal/cli (TODO(docsync-action/cli)).
type PipelineRuntime interface {
	Run(input PipelineRunInput) (*schemas.RunOutcome, error)
}

// PipelineRuntimeOptions are the composition options the port forwards to the
// factory (INV-8: env is the OPENROUTER_*-stripped map; WriterOverride is the
// report-only writer stub when report-only is in force).
type PipelineRuntimeOptions struct {
	// RepoRoot is the absolute repository root (the checked-out worktree).
	RepoRoot string
	// Repo is the repository run-key identity (owner/name).
	Repo string
	// Env is the model-layer env (already stripped to OPENROUTER_* by the
	// caller — INV-8).
	Env map[string]string
	// Clock is the injected ISO-8601 clock.
	Clock func() string
	// WriterOverride is the report-only writer stub; nil for full runs.
	WriterOverride orchestrator.WriterStagePort
}

// PipelineRuntimeFactory composes a PipelineRuntime for one repository (the
// ported `createCliPipelineRuntime`). Implementation: internal/cli
// (TODO(docsync-action/cli)).
type PipelineRuntimeFactory interface {
	Create(opts PipelineRuntimeOptions) (PipelineRuntime, error)
}
