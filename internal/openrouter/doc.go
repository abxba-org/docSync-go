// Package openrouter is the Go port of packages/openrouter — the model-access
// layer of docSync (ARCH §10; D9; D10): every AI inference in docSync goes
// through OpenRouter as bounded leaf stages with schema-validated I/O; the
// routing layer decides WHICH model serves each risk bucket from versioned,
// reviewable DATA (ARCH §10.1/§10.3: the bucket → model mapping is declarative
// and versioned — a policy change, not a code change).
//
// Layering (plan.md §3): openrouter is L3 and MUST NOT import internal/policy
// or internal/verifier (L4). It imports internal/schemas + internal/docutil
// only — plus, in the orchestrator/adapters units' files, the plain
// internal/{workspace,gitx,graph} helpers they wrap. The two policy symbols
// the package eventually needs (policy.CompiledPolicyWorkflow and
// policy.ResolveDecisionID) are declared as LOCAL SEAMS in models.go with TODO
// comments naming internal/policy; they are swapped for the real imports in a
// later wave once internal/policy is stable.
//
// This package is migrated by concurrent units with disjoint file ownership
// (CONVENTIONS.md: one writer per file). File map of the current wave:
//
//	doc.go                          ← this file (package doc; Step 0, client-core unit)
//	errors.go                       ← src/errors.ts
//	transport.go                    ← src/transport.ts
//	credentials.go                  ← src/credentials.ts
//	structured.go                   ← src/structured.ts
//	client.go                       ← src/client.ts
//	models.go                       ← src/models.ts            (models/roles unit)
//	roles.go                        ← src/roles.ts             (models/roles unit)
//	provenance.go                   ← src/routing/provenance.ts (models/roles unit)
//	routing-errors.go               ← src/routing/errors.ts    (models/roles unit)
//	routing-policy.go               ← src/routing/policy.ts    (routing/extras unit)
//	routing-replacement.go          ← src/routing/replacement.ts
//	routing-provenance.go           ← src/routing/provenance.ts
//	adapter-decision-engine.go      ← src/adapters/decision-engine.ts  (adapters unit)
//	adapter-judge.go                ← src/adapters/judge.ts
//	adapter-patch-writer.go         ← src/adapters/patch-writer.ts
//	adapter-benchmarks.go           ← src/adapters/benchmarks.ts
//	adapter-claim-extractor.go      ← src/adapters/claim-extractor-adapter.ts
//	adapter-drift-auditor.go        ← src/adapters/drift-auditor-adapter.ts
//	adapter-policy-architect.go     ← src/adapters/policy-architect-adapter.ts
//	writer-stage.go                 ← src/orchestrator/writer-stage.ts (writer-stage unit)
//	testing-transports.go           ← src/testing/transports.ts (recorded test kit)
//	*_test.go                       ← the matching test suites per unit
//
// Failure model (D10): the client never fabricates a result. Total model
// exhaustion returns a typed *TotalInferenceFailure and the CALLER degrades to
// advisory/REVIEW; schema violations return a typed schema-abstain result
// (never thrown, never hand-patched into compliance); transport/network
// failures surface as typed *TransportError values. Every error this package
// produces is a coded error through docutil (client-core: docutil.CodeModel /
// EDOCSYNC_MODEL for the gateway error taxonomy) with the TS class code and
// message text preserved verbatim.
//
// Guards (invariants 8/10/13): every outgoing body is scanned for API-key
// material BEFORE I/O; untrusted repository content is delimiter-wrapped with a
// fixed containment instruction; model output is untrusted data — nothing in
// this package grants authority from model output, only typed data.
package openrouter
