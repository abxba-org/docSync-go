package verifier

// Ported from packages/verifier/src/tier1/types.ts (P4-T02..T07; PLAN §5.4
// items 2–7; ARCH §4.8 Tier 1).
//
// The verifier OWNS NOTHING ABOUT PERMISSIONS: `Tier1Input` is
// application-owned data injected at the orchestrator edge — the manifest
// allowlist is derived from the manifest (never from model output) by the
// application, the effective target mode is resolved by the application (D2
// re-check input), and the document content is pre-read. The verifier is PURE
// over these inputs: no fs, no git writes, no clock of its own (the clock is
// injected), no network (a reachability probe is a caller PORT — the verifier
// itself never opens a socket).
//
// Optional-field mapping (CONVENTIONS.md "Types & serialization"): TS
// `field?: T` / `T | null` become pointer fields; a nil pointer is "absent".
// `ReadonlySet<string>` becomes a map[string]bool set; `Readonly<Record>`
// becomes a map.

import (
	"github.com/DUDE616/docSync-go/internal/patch"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// LinkCheckInputs is the doc-index + external-URL data link-resolution needs
// (injected).
type LinkCheckInputs struct {
	// KnownDocs maps repo-relative path → content (the docs index snapshot the
	// application assembled). Relative file links resolve against this;
	// cross-doc `#anchor` fragments resolve against the target's headings.
	KnownDocs map[string]string
	// ExternalAllowlist is the set of external URLs (`http://`/`https://`)
	// the repository declares as trusted.
	ExternalAllowlist map[string]bool
	// IsReachable is the optional reachability PORT (application-owned).
	// Absent (nil) ⇒ non-allowlisted external URLs fail closed. The verifier
	// never performs network I/O itself.
	IsReachable func(url string) (bool, error)
}

// ExampleBlock is one fenced code block handed to an example checker (1-based
// opening fence line).
type ExampleBlock struct {
	Language string
	Code     string
	Line     int
}

// ExampleCheckResult is the result one example checker reports for one block.
type ExampleCheckResult struct {
	Ok     bool
	Detail *string
}

// ExampleCheckerPort is the optional future-tooling hook: invoked per fenced
// block (any declared language) with its language + code; a non-ok result
// fails examples-parse.
type ExampleCheckerPort func(block ExampleBlock) (ExampleCheckResult, error)

// Tier1Input is the inputs ONE Tier-1 run needs (application-owned; see the
// package doc).
type Tier1Input struct {
	// Patch is the patch under verification (schema-validated upstream).
	Patch *schemas.DocPatch
	// DocContent is the current worktree/head content of `patch.doc_path`;
	// nil = the doc is absent (TS null).
	DocContent *string
	// ManifestAllowlist holds the writable paths derived from the manifest BY
	// THE APPLICATION (invariant 3).
	ManifestAllowlist map[string]bool
	// TargetMode is the effective mode of doc_path resolved by the
	// application (D2 re-check input). The TS union is WorkspaceMode —
	// DocMode plus the `external` pseudo-mode — which renders through
	// schemas.DocMode here (the manifest mode union has no 'external'
	// member; the tier-1 ownership floor treats it as read-only, exactly like
	// the verification stage's docModeExternal).
	TargetMode schemas.DocMode
	// TriggeringDiffStats is the triggering code-diff size for the minimality
	// ratio (P3-T12); optional.
	TriggeringDiffStats *patch.DiffStats
	// MinRatio overrides the minimality bound; nil defaults to
	// patch.DefaultMinimalityMaxRatio (V-P3-6).
	MinRatio *float64
	// LinkInputs carries the docs index + external-URL allowlist + optional
	// reachability port.
	LinkInputs *LinkCheckInputs
	// ExampleChecker is the optional future tooling hook for examples-parse.
	ExampleChecker ExampleCheckerPort
	// Git is git read access + the rev whose tree holds the doc — OPTIONAL.
	// Supplied, applyDocPatch may read head content itself; absent, the check
	// works from `DocContent` alone (no real git needed). Never used for
	// writes.
	Git patch.CatFilePort
	// Rev is the rev for the optional git port; REQUIRED when `Git` is
	// supplied (nil = TS undefined).
	Rev *string
	// BaseContent is the writer's base snapshot (enables the race 3-way merge
	// path); optional.
	BaseContent *string
	// IsRunApplied is the persisted run-state port (application-owned
	// idempotency state, P3-T11); nil ⇒ content-level idempotency only.
	IsRunApplied patch.IsRunAppliedPort
	// Now is the INJECTED clock (provenance `appliedAt`; never a local Date).
	Now func() string
}

// Tier1Context is what later tiers / the pipeline consume from the Tier-1 run.
type Tier1Context struct {
	// ApplyOutcome is the application outcome of the simulated apply (always
	// present when the apply check ran).
	ApplyOutcome *patch.ApplyOutcome
	// PatchedContent is the patched content — for `applied`, the engine's
	// result; for `already-applied`, the unchanged document (the patch's
	// effect is in it). Nil when no simulation produced content (TS: the key
	// is omitted).
	PatchedContent *string
	// Minimality is the verifier's own minimality measurement (present when
	// the check ran, not skipped).
	Minimality *patch.MinimalityMeasurement
}

// Tier1Result is the Tier-1 result: one record per check, ALL EIGHT always
// emitted.
type Tier1Result struct {
	Tier    int
	Status  string
	Checks  []schemas.VerificationCheck
	Context Tier1Context
}

// tier1DetailPtr returns a pointer to s for the optional `detail` field the
// TS records always set on these paths (schemas.VerificationCheck.Detail is
// a *string). Named for this unit only — concurrent units own their own
// helpers (CONVENTIONS.md: one writer per file).
func tier1DetailPtr(s string) *string { return &s }
