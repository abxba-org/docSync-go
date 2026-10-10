package verifier

// Tier-2 types (port of tier2/types.ts; P4-T08..T12; PLAN §5.4; ARCH §4.8
// Tier-2): the inputs, evidence context, and results of the evidence-backed
// per-claim tier.
//
// The verifier OWNS NOTHING ABOUT PERMISSIONS: Tier2Input is application-owned
// data injected at the orchestrator edge — the doc content and the Tier-1
// patched content are pre-read, the workspace sources / head SHAs come from the
// manifest-scoped workspace, the claim-store entries and contract vector come
// from Phase 1, and the clock is injected (never a local Date). The tier is
// PURE over these inputs: no fs, no git writes, no network, no clock of its
// own.
//
// Type provenance: DocPatch, Claim, VerificationClaim and VerificationCheck
// come from internal/schemas (doc-patch.go, state.go, verification-report.go);
// ContractVector comes from internal/contracts (classify.go, the port of
// core/contracts/classify.ts) and DocumentGraph from internal/graph. The TS
// imported both from @docsync/core; the Go split mirrors the package layout
// (contracts intentionally does not import graph, so the coverage check adapts
// the injected graph to the contracts package's coverage entry point — see
// tier2_coverage.go).

import (
	"github.com/DUDE616/docSync-go/internal/contracts"
	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// Tier2Input is the input ONE Tier-2 run needs (application-owned; see the
// module doc). Optional slices/maps are nil when absent; the nil-vs-empty
// distinction is load-bearing wherever the TS code tested `=== undefined`
// (Claims, ContractNames).
type Tier2Input struct {
	// Patch is the patch under verification (schema-validated upstream).
	Patch *schemas.DocPatch
	// DocContent is the current worktree/head content of patch.doc_path;
	// nil = the doc is absent (TS: string | null).
	DocContent *string
	// PatchedContent is the content after the Tier-1 simulated apply — the
	// text claims are checked against; nil = the apply produced no text.
	PatchedContent *string
	// Claims are the claim-store entries relevant to this document
	// (over-deletion guard evidence); nil = no snapshot supplied.
	Claims []schemas.Claim
	// WorkspaceSources maps repo-relative path → content (workspace-restricted
	// binding); nil = none injected.
	WorkspaceSources map[string]string
	// SourceHeadShas pins the provenance (head SHA) per source file.
	SourceHeadShas map[string]string
	// ContractVector is the per-doc contract classification vector (P1-T12;
	// ARCH §4.4); nil = none.
	ContractVector *contracts.ContractVector
	// ContractNames are the contract names declared on the document (graph
	// mapping order); nil/empty = none declared.
	ContractNames []string
	// ContractKinds maps contract name → kind, as declared in concepts.yml
	// `contractKinds`; nil = none.
	ContractKinds map[string]schemas.ContractKind
	// Graph is the document graph snapshot (D7), for anchor/lookup context;
	// nil = no graph supplied.
	Graph *graph.DocumentGraph
	// Now is the INJECTED clock (provenance `verifiedAt`; never a local
	// Date). Tier 2 never reads it — it is carried for provenance symmetry
	// with the other tiers, exactly as in the TS runner.
	Now func() string
}

// DecomposedClaimKind is the closed V1 kind vocabulary for decomposed
// per-claim checking (TS: 'signature' | 'value' | 'error_code' | 'behavioral' |
// 'other').
type DecomposedClaimKind string

// The closed-vocabulary kinds in TS declaration order.
const (
	DecomposedClaimKindSignature  DecomposedClaimKind = "signature"
	DecomposedClaimKindValue      DecomposedClaimKind = "value"
	DecomposedClaimKindErrorCode  DecomposedClaimKind = "error_code"
	DecomposedClaimKindBehavioral DecomposedClaimKind = "behavioral"
	DecomposedClaimKindOther      DecomposedClaimKind = "other"
)

// DecomposedClaimKindValues lists every vocabulary kind in canonical order.
var DecomposedClaimKindValues = []DecomposedClaimKind{
	DecomposedClaimKindSignature,
	DecomposedClaimKindValue,
	DecomposedClaimKindErrorCode,
	DecomposedClaimKindBehavioral,
	DecomposedClaimKindOther,
}

// DecomposedClaim is one claim decomposed from the patched region (PLAN §5.3).
// Behavioral predicates carry kind 'behavioral' — they abstain to suggest in V1
// (OD-R09), never auto-verified.
type DecomposedClaim struct {
	// Index is the position in the decomposition order; the tracker's key
	// (0-based, dense).
	Index int `json:"index"`
	// Text is the claim text as decomposed from the patched region.
	Text string `json:"text"`
	// Kind is the closed-vocabulary kind driving which Tier-2 check applies.
	Kind DecomposedClaimKind `json:"kind"`
	// HunkIndex is the patch hunk the claim was decomposed from.
	HunkIndex int `json:"hunk_index"`
	// ExistingClaimID is the pre-existing claim-store id, when the hunk edits
	// a known claim (absent when the hunk carries no claim_ids entry — claim
	// ids are schema-enforced non-empty, so the empty string never occurs).
	ExistingClaimID string `json:"existing_claim_id,omitempty"`
	// Line is the 1-based line in the patched document, when resolvable.
	Line *int `json:"line,omitempty"`
}

// ClaimBindingVia is the anchor-kind vocabulary (closed): 'claim-store' (a
// recorded store anchor), 'workspace-token' (a deterministic token match in
// the injected workspace), or 'none' (unbound). The V1 binding surface is
// TOKEN-LEVEL — semantic surfaces (OpenAPI path/operation, config key with
// RESOLVED value, test-name structure) are Phase-11 analyzer territory (A4
// posture: declared, never silent).
type ClaimBindingVia string

// The anchor-kind vocabulary values.
const (
	BindingViaClaimStore     ClaimBindingVia = "claim-store"
	BindingViaWorkspaceToken ClaimBindingVia = "workspace-token"
	BindingViaNone           ClaimBindingVia = "none"
)

// ClaimBinding is whether (and how) one decomposed claim bound to workspace
// evidence.
type ClaimBinding struct {
	// Index is the decomposed-claim index this binding describes.
	Index int `json:"index"`
	// Bound is true when the claim anchored to evidence (else routes
	// `unanchored`).
	Bound bool `json:"bound"`
	// File is the repo-relative file the claim bound to, when bound.
	File string `json:"file,omitempty"`
	// Line is the 1-based line in File, when resolvable.
	Line *int `json:"line,omitempty"`
	// Via is the anchor-kind vocabulary value (closed).
	Via ClaimBindingVia `json:"via"`
	// Detail is deterministic evidence detail, e.g. `src/payments/refund.ts:42`.
	Detail string `json:"detail,omitempty"`
}

// Tier2Context is what later tiers / the pipeline consume from the Tier-2 run.
type Tier2Context struct {
	// Claims are the per-claim statuses (PLAN §5.3) — the auto-eligibility
	// input (OD-R09).
	Claims []schemas.VerificationClaim `json:"claims"`
	// Decomposed is the decomposition this tier checked (replayable evidence).
	Decomposed []DecomposedClaim `json:"decomposed"`
	// Bindings is one binding per decomposed claim (unanchored never auto —
	// PLAN §5.6/ARCH §4.8).
	Bindings []ClaimBinding `json:"bindings"`
}

// Tier2Result is the Tier-2 result: one record per Tier-2 check, ALL always
// emitted (seven in canonical order).
type Tier2Result struct {
	// Tier is always schemas.Tier2 (2).
	Tier int `json:"tier"`
	// Status is 'pass' or 'fail' (schemas.CheckPass / schemas.CheckFail).
	Status string `json:"status"`
	// Checks are the check records in canonical order.
	Checks []schemas.VerificationCheck `json:"checks"`
	// Context is the per-claim surface later tiers and the pipeline consume.
	Context Tier2Context `json:"context"`
}

// tier2StringPtr builds the optional-string pointers the check records carry
// (`detail` is always set by every Tier-2 record, so the TS optional field is
// materialized here; a nil pointer serializes as an absent key, matching the
// TS `...(detail === undefined ? {} : { detail })` spread).
func tier2StringPtr(s string) *string { return &s }
