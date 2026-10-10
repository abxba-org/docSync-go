// Package workspace ports the docSync constrained-workspace layer (Phase 3,
// P3-T02..T05; ARCH §4.7, §3.3, §3.7, §11.4; D2) from TypeScript to Go.
//
// The workspace is the writer's ENTIRE universe (ARCH §4.7): a bounded,
// deterministic assembly of exactly seven components
// (WORKSPACE_COMPONENTS) — git_diff, change_analysis, jev_decision,
// manifest_policy, source_files, relevant_documents, project_instructions —
// that the writer adapter later wraps as untrusted content. Anything not in
// it cannot be patched: invariants 2 (never code files) and 3 (never outside
// the manifest allowlist) are enforced structurally by what the assembler
// admits, and lock layer 2 (§11.4) by the per-path readonly markers plus the
// canonical writer prompt's protected-target prohibition (duties.go).
//
// Ported files (one TS source per Go file):
//
//	types.go     <- core/src/workspace/types.ts    (seven-component tuple, modes,
//	                                                    WorkspaceSection, caps,
//	                                                    BatchInfo, ConstrainedWorkspace,
//	                                                    WorkspaceAssemblyError)
//	assemble.go  <- core/src/workspace/assemble.ts (AssembleWorkspace — the
//	                                                    seven-component assembler,
//	                                                    cap enforcement, D2 marking)
//	batch.go     <- core/src/workspace/batch.ts    (BatchRelatedDocs —
//	                                                    deterministic session
//	                                                    planning, BatchConfigError)
//	duties.go    <- core/src/workspace/duties.ts   (FiveDuties,
//	                                                    BuildWriterSystemPrompt,
//	                                                    ValidateWriterOutputContract)
//	render.go    <- core/src/workspace/render.ts   (frames, containment escape,
//	                                                    RenderWorkspace)
//
// Import DAG (plan.md §3): workspace is layer 3 and imports only lower layers —
// docutil/schemas (L0), graph/retrieval (L2). It NEVER performs fs/git I/O and
// NEVER reads a local clock: source/doc contents are caller-fetched, the clock
// is injected, and size caps are bootstrap data enforced BY the assembler and
// REPORTED (never silently dropped). Same inputs (including the injected clock
// value) therefore yield a byte-identical rendered payload — the V-P3-1
// determinism contract.
//
// Fail-closed discipline (D8): a missing, malformed or duplicated required
// input returns a *WorkspaceAssemblyError naming the affected component; a
// partial workspace is never assembled silently. An input that is PRESENT but
// semantically empty (” diff, empty arrays, ” instructions) or explicitly
// null (a nil decision pointer) still assembles, carrying an explicit
// empty/none marker and reason — the assembler NEVER omits a component.
package workspace
