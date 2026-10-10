package gitpr

// Ported from packages/core/src/gitpr/doc-pr-flow.ts (P6-T25 provenance gate,
// P6-T26 state-CAS participation, P6-T28 flow composition; PLAN §7.3 lines
// 1159-1187; ARCH §12.2-§12.5; invariants 1/2/3/4/7/10/11).
//
// This is the Phase-6 write path's ORCHESTRATION: it composes the prior waves'
// seams — branch factory, commit assembly with its INV-10 gate, the layer-4 PR
// filter, the safety-incident log, the rate governors, the marker upsert
// client, the stale-PR supersede decision, the no-op drop evaluator, the
// merge-order ledger over the CAS state store — into ONE deterministic
// create-or-update flow. NO model inference anywhere — every step is
// deterministic orchestrator code consuming pre-derived evidence; the flow
// never re-derives a fact a prior stage already recorded (D3). The runtime
// trace makes that property OBSERVABLE: every port call lands in the trace
// under a seam that is never one of LLMSeamNames.
//
// Step order (each step GATED — the flow aborts BEFORE any later side effect):
//
//  0. DIRTY-WORKTREE PRE-FLIGHT (M5): git.StatusPorcelain must be EMPTY before
//     anything else — the flow never creates refs or ships from a dirty
//     worktree (aborts `dirty-worktree`, before ANY ref creation).
//  1. PROVENANCE GATE (P6-T25/INV-10 + P9-T06): AssembleDocCommit runs FIRST
//     after the pre-flight and its *ProvenanceMissingError aborts the flow with
//     a `provenance_missing` safety incident BEFORE any platform call. BOTH the
//     FlowProvenance run evidence AND the `docsync.provenance/v1` record
//     (input.ProvenanceRecord) are required; either missing refuses through
//     the same typed path, and the record is rendered into the PR body via
//     provenance.RenderProvenanceSummary (D5).
//  2. CONTENT GUARD (P6-T27/INV-7): the assembled plan's changed paths and the
//     patches are guarded — a manifest/policy edit or a machine-claimed mode
//     transition to `auto` aborts (typed `guard-violation`) with
//     `manifest_or_policy_edit` / `machine_mode_transition` incidents, still
//     before any git write or platform call.
//  3. PARTIAL-PASS CLASSIFICATION (P6-T10/D4): every patch must map to a
//     document of the frozen outcome (a patch with NO outcome row is an
//     evidence gap → typed `inconsistent-input`, fail closed); a patch whose
//     document did NOT pass verification is DROPPED in-flow and reported via
//     the partial-pass report in the PR body. Nothing verification-passing
//     survives → result `no-op` (no PR noise, no platform call).
//  4. BRANCH + COMMIT: the doc branch is created via the branch factory (INV-1
//     guard; `-2` disambiguator for foreign collisions) and the doc commit is
//     written via WriteDocCommit ON that branch.
//  5. REAL REBASE (M3a): the doc branch is REBASED onto the CURRENT
//     default-branch head via GitClient.RebaseBranch. A conflict is aborted
//     internally by the client and the flow aborts typed `rebase-conflict` —
//     NEVER a forced apply (A9).
//  6. NO-OP DROP (M3b): each surviving patch is re-evaluated against the
//     DEFAULT-branch head content via EvaluateNoOPDrop — a byte-identical
//     no-op patch is dropped from the PR set and reported in `droppedNoOps`;
//     when ALL patches drop, the result is `no-op` with NO platform call.
//  7. RE-VERIFY GATE (M3c; A9): when the default branch has ADVANCED past the
//     run's analysis anchor (input.RunHeadSha; local runs fall back to the
//     resolved run head) the merged content on the branch must RE-PASS
//     verification through the injected Reverify hook — verdict 'pass' AND
//     auto_eligible. A failed re-verify abstains typed `reverify-failed`; a
//     MOVED base with an ABSENT hook fails CLOSED typed `reverify-unavailable`.
//     An unchanged base skips the gate.
//  8. PR FILTER (layer 4, INV-2/3/4): PrFilter.FilterBranchDiff re-diffs the
//     ASSEMBLED (post-rebase) branch against the default branch. Any violation
//     aborts (typed `filter-violation`) with `out_of_allowlist_path` /
//     `protected_path_patch` incidents, BEFORE any platform call. NO branch
//     cleanup on abort: the branch STAYS for forensics.
//  9. SELF-TRIGGER SUPPRESSION (P6-T19): a run whose source PR is docsync's OWN
//     doc PR aborts typed `self-triggered` with NO incident and no platform
//     call (the canonical check lives in self-trigger.go).
// 10. UPSERT DECISION (M2/M4): the marker-keyed OPEN lookup plus the
//     merge-order ledger's open entry feed EvaluateSupersedence: same key +
//     open prior → UPDATE; different key + open prior → SUPERSEDE the stale PR
//     and create the replacement; NO open prior → create (the closed-prior
//     same-key case being a legitimate RE-CREATION on a disambiguated branch).
// 11. OPEN-PR CAP (P6-T06): the cap governs CREATION only — the update path of
//     a same-key prior SKIPS the cap; the create and supersede-then-create
//     paths evaluate it (downgrade at/over cap to report-only with the exact
//     `patch ready, apply manually` message).
// 12. HOURLY BURST (P6-T07): the burst limiter is HOISTED into the
//     CreateDocPrFlow closure — ONE instance shared by all Run calls, so burst
//     state survives runs and INV-11 is real. An over-budget admission AWAITS
//     via the injected delay primitive (queue-not-fail).
// 13. MERGE-ORDER LEDGER (P6-T18/T26/R15): the ledger record IS the CAS point.
//     A cas-conflict result returns the shipped result annotated
//     `casConflict: true, rerunRequired: true` — the PR stands, the LOSER
//     re-runs evaluation against a fresh read; the ledger NEVER overwrites the
//     winner (R15). Supersede runs record the stale PR's `open → superseded`
//     transition through the closed transition table.
//
// Discriminated-union convention (repo): every closed TS union is rendered as
// ONE Go struct carrying a `Kind`/`Outcome`/`Action` tag with optional fields
// projected through pointers (nil = TS `null`/absent) — the same shape
// rebase-gate.go, supersede.go and merge-ledger.go use.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

/* -------------------------------------------------------------------- errors */

// EDOCSYNCDocPrFlowConfig preserves the TS DocPrFlowConfigError.code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const EDOCSYNCDocPrFlowConfig = "EDOCSYNC_DOC_PR_FLOW_CONFIG"

// EDOCSYNCDocPrFlowInput preserves the TS DocPrFlowInputError.code verbatim.
const EDOCSYNCDocPrFlowInput = "EDOCSYNC_DOC_PR_FLOW_INPUT"

// docPrFlowConfigError is the fail-closed refusal of a malformed flow DEPS (a
// wiring bug, fail loud).
func docPrFlowConfigError(message string) error {
	return docutil.New(EDOCSYNCDocPrFlowConfig, message)
}

// docPrFlowInputError is the fail-closed refusal of a malformed per-run flow
// INPUT (a caller bug).
func docPrFlowInputError(message string) error {
	return docutil.New(EDOCSYNCDocPrFlowInput, message)
}

/* -------------------------------------------------------------------- shapes */

// FlowSourcePr is the source PR a run documents; HeadBranch/Author feed the
// self-trigger key when known.
type FlowSourcePr struct {
	// Number is the source PR number.
	Number int
	// HeadSha is the source PR's FULL head commit sha (branch naming +
	// idempotency key).
	HeadSha string
	// HeadBranch is the source PR's head branch name, when known (self-trigger
	// key, half 1). Nil = absent.
	HeadBranch *string
	// Author is the source PR's author identity, when known (self-trigger key,
	// half 2). Nil = absent.
	Author *string
}

// DocPrFlowBudgets are the per-run rate budgets (PLAN §7.3 defaults: cap 3,
// burst 10/hour). A nil field means "use the default".
type DocPrFlowBudgets struct {
	// MaxOpen is the max open docsync PRs per repo — integer in [1, 10];
	// default 3 (nil).
	MaxOpen *int
	// MaxPerHour is the max doc-PR admissions per rolling hour — integer >= 1;
	// default 10 (nil).
	MaxPerHour *int
}

// FlowModelBinding is one routed model binding of FlowProvenance.
type FlowModelBinding struct {
	Role     string
	Model    string
	Provider *string
}

// FlowProvenance is the run's provenance evidence (INV-10): present or the
// flow refuses to ship. The flow itself reads only RunID (the presence/sanity
// gate); PolicyVersion/ModelBindings are carried for contract fidelity.
type FlowProvenance struct {
	// RunID must equal the flow input's RunID (a record naming another run is
	// incoherent).
	RunID         string
	PolicyVersion int
	ModelBindings []FlowModelBinding
}

// DocPrFlowInput is the input of DocPrFlow.Run.
type DocPrFlowInput struct {
	RunID string
	// SourcePr is the PR the run documents; nil for local runs.
	SourcePr *FlowSourcePr
	// Patches are the verified doc patches to carry.
	Patches []schemas.DocPatch
	// StateUpdates are the committed-state carriage updates; MUST live under
	// `.docsync/state/`.
	StateUpdates []CommitStateUpdate
	// Provenance (INV-10) is the run-level provenance evidence; nil aborts the
	// flow before ANY side effect.
	Provenance *FlowProvenance
	// ProvenanceRecord is the run's `docsync.provenance/v1` record (P9-T06; D5)
	// — the canonical evidence the PR body renders. OPTIONAL at the type level
	// (pre-P9 callers keep compiling) but REQUIRED by the gate: nil aborts the
	// flow through the SAME typed refusal + `provenance_missing` incident as
	// Provenance (INV-10).
	ProvenanceRecord *schemas.Provenance
	// Outcome is the frozen pipeline outcome (evidence consumed, never
	// re-derived).
	Outcome schemas.RunOutcome
	// Canonical is the canonical report (consumed verbatim into the PR body).
	Canonical schemas.CanonicalReport
	// Manifest is the parsed manifest — the allowlist/mode authority (D7).
	Manifest *schemas.Manifest
	// Budgets are the per-run rate budgets (nil = defaults).
	Budgets *DocPrFlowBudgets
	// RunHeadSha is the default-branch head sha the pipeline's analysis was
	// anchored to — the M3c re-verify gate's baseline. Nil ⇒ absent; local runs
	// fall back to the run head from HEAD, a PR run without it skips the gate.
	RunHeadSha *string
	// DocContents is the applied whole-file content per patch doc_path (the
	// verification stage's resultContent); nil for state-only assemblies.
	DocContents map[string]string
}

// DocPrFlowDeps are the deps of CreateDocPrFlow — everything injected, nothing
// global.
type DocPrFlowDeps struct {
	// Git is the typed git transport (W2 seam).
	Git *gitx.GitClient
	// Platform is the platform port — the ONLY side-effect surface beyond
	// git/disk.
	Platform PrPlatformPort
	// Credentials (INV-8): validated by the composed client, then never
	// retained.
	Credentials   PrCredentials
	Repo          string
	DefaultBranch string
	// Store is the CAS state store surface the ledger persists through
	// (P6-T26); the flow accesses it only via the merge ledger.
	Store MergeLedgerStateStore
	// Incidents is the safety-incident sink.
	Incidents *IncidentLog
	// Clock is the ISO-8601 clock (P19: stamps records only; the burst limiter
	// derives nowMs from it).
	Clock func() string
	// Delay is the injected delay for the burst limiter's queue-not-fail wait
	// (nil = a real sleep; tests: fake clock advance).
	Delay BurstDelay
	// Reverify is the M3c re-verify pass (A9): given a patch and the MERGED
	// content on the rebased branch, returns the verification report the merged
	// result must satisfy — verdict 'pass' AND auto_eligible. ABSENT + a moved
	// default branch → the flow fails CLOSED.
	Reverify ReverifyPort
	// Rng is reserved for the rate-limit backoff composition (P6-T09) wired by
	// the Phase-8 transport; accepted at the flow seam for injection parity.
	Rng func() float64
	// OwnIdentity is docsync's own author identity in this context
	// (self-trigger key, half 2).
	OwnIdentity string
	// Apply is the disk seam for WriteDocCommit — the ONLY sanctioned writer.
	Apply DocFileApplier
}

/* -------------------------------------------------------- violations + trace */

// PrFlowViolation is normalized violation evidence carried by an aborted flow
// result. Rule is the closed filter/guard rule union (a PrFilterRule or a
// MachinePrContentRule — both are string-typed in Go, so the wire value is
// carried verbatim).
type PrFlowViolation struct {
	Path   string
	Rule   string
	Detail string
}

// PrFlowAbortReason is why a flow aborted (closed union). `dirty-worktree`
// (M5) fires before ANY ref creation; `rebase-conflict` / `reverify-failed` /
// `reverify-unavailable` are the M3 merge-order gate abstentions (A9 — abstain
// to REVIEW, never force).
type PrFlowAbortReason string

const (
	PrFlowAbortProvenanceMissing   PrFlowAbortReason = "provenance-missing"
	PrFlowAbortGuardViolation      PrFlowAbortReason = "guard-violation"
	PrFlowAbortFilterViolation     PrFlowAbortReason = "filter-violation"
	PrFlowAbortSelfTriggered       PrFlowAbortReason = "self-triggered"
	PrFlowAbortInconsistentInput   PrFlowAbortReason = "inconsistent-input"
	PrFlowAbortDirtyWorktree       PrFlowAbortReason = "dirty-worktree"
	PrFlowAbortRebaseConflict      PrFlowAbortReason = "rebase-conflict"
	PrFlowAbortReverifyFailed      PrFlowAbortReason = "reverify-failed"
	PrFlowAbortReverifyUnavailable PrFlowAbortReason = "reverify-unavailable"
)

// PrFlowDowngradeReason is why a flow downgraded to report-only.
type PrFlowDowngradeReason string

const (
	PrFlowDowngradeOpenPrCapReached PrFlowDowngradeReason = "open-pr-cap-reached"
)

// PrFlowTraceSeam is one runtime-trace seam (m4): where a port call the flow
// made landed. The union is CLOSED over deterministic seams — LLMSeamNames
// names the seams a generative model could occupy, and none of them is here.
type PrFlowTraceSeam string

const (
	PrFlowTraceGit       PrFlowTraceSeam = "git"
	PrFlowTracePlatform  PrFlowTraceSeam = "platform"
	PrFlowTraceState     PrFlowTraceSeam = "state"
	PrFlowTraceLedger    PrFlowTraceSeam = "ledger"
	PrFlowTraceIncidents PrFlowTraceSeam = "incidents"
)

// PrFlowTraceEntry is one runtime-trace entry: ONE port call, recorded by the
// composed flow.
type PrFlowTraceEntry struct {
	Seam PrFlowTraceSeam
	Op   string
}

// LLMSeamNames is the P19 boundary vocabulary: the seams a GENERATIVE model
// could occupy (`decision`, `writer`, `judge`). The composed flow's runtime
// trace must NEVER contain one — exported for the V-P6-19 tripwire and for
// callers asserting the same property over their own traces.
var LLMSeamNames = map[string]bool{
	"decision": true,
	"writer":   true,
	"judge":    true,
}

// PrFlowNoOpDrop is one no-op patch dropped from the PR set (M3b; PLAN §12.3).
type PrFlowNoOpDrop struct {
	DocPath string
	// Detail is the evaluator's rule-naming detail (evidence, never a bare
	// flag).
	Detail string
}

/* ------------------------------------------------------------ the result union */

// PrFlowResultKind is the discriminated-union tag of PrFlowResult.
type PrFlowResultKind string

const (
	PrFlowKindCreated              PrFlowResultKind = "created"
	PrFlowKindUpdated              PrFlowResultKind = "updated"
	PrFlowKindNoOp                 PrFlowResultKind = "no-op"
	PrFlowKindDowngradedReportOnly PrFlowResultKind = "downgraded-report-only"
	PrFlowKindAborted              PrFlowResultKind = "aborted"
)

// PrFlowResult is the closed flow result union rendered as one tagged struct
// (the repo's discriminated-union convention). Only the fields belonging to the
// selected Kind are populated; pointer fields are nil when absent (the TS
// `null`/omitted projection).
//
//   - shipped (PrFlowKindCreated / PrFlowKindUpdated): PR, Branch, BranchCreated,
//     IdempotencyKey, CommitSha, PartialPass, DroppedNoOps, CasConflict,
//     RerunRequired, optional LedgerHash/LedgerDetail, Trace.
//   - no-op (PrFlowKindNoOp): optional Branch, DroppedNoOps, PartialPass, Trace.
//   - downgraded-report-only (PrFlowKindDowngradedReportOnly): Reason,
//     Message, OpenCount, MaxOpen, Branch, Trace.
//   - aborted (PrFlowKindAborted): Reason, Detail, Violations, Trace.
type PrFlowResult struct {
	Kind PrFlowResultKind

	// shipped
	PR             *PrRef
	Branch         string
	BranchCreated  bool
	IdempotencyKey string
	// CommitSha is the new doc-commit sha; nil when the re-run's write was a
	// content no-op.
	CommitSha    *string
	PartialPass  *PartialPassResult
	DroppedNoOps []PrFlowNoOpDrop
	// LedgerHash is the merge-ledger hash when the CAS record landed; nil when
	// skipped (already open).
	LedgerHash    *string
	CasConflict   bool
	RerunRequired bool
	// LedgerDetail is the CAS conflict evidence when CasConflict.
	LedgerDetail *string

	// downgraded-report-only + aborted: the wire reason value.
	Reason string
	// downgraded-report-only
	Message   string
	OpenCount int
	MaxOpen   int

	// aborted
	Detail     string
	Violations []PrFlowViolation

	// all variants: one entry per port call the run made (m4).
	Trace []PrFlowTraceEntry
}

/* ------------------------------------------------------- the flow + factory */

// DocPrFlow is the composed doc-PR flow — Run composes the gated steps in the
// documented order.
type DocPrFlow interface {
	Run(ctx context.Context, input DocPrFlowInput) (PrFlowResult, error)
}

// flowBurstClock adapts the injected ISO-8601 clock to the governors' BurstClock
// (nowMs derivation). The TS nowMs closure throws on a non-ISO clock; the Go
// port records a sticky config error instead (NowMs cannot fail), surfaced by
// Err after the admission so the flow fails closed exactly where the burst
// limiter runs.
type flowBurstClock struct {
	clock func() string
	mu    sync.Mutex
	err   error
}

// NowMs parses clock() to epoch milliseconds; a non-ISO value latches a config
// error (fail closed) and yields 0.
func (c *flowBurstClock) NowMs() int64 {
	parsed, err := time.Parse(time.RFC3339Nano, c.clock())
	if err != nil {
		c.mu.Lock()
		if c.err == nil {
			c.err = docPrFlowConfigError(
				"deps.clock must return an ISO-8601 string (the burst limiter derives nowMs from it)")
		}
		c.mu.Unlock()
		return 0
	}
	return parsed.UnixMilli()
}

// Err returns the latched clock-config error, if any.
func (c *flowBurstClock) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// docPrFlow is the constructed flow. The burst limiter (+ its budget) is
// instance state, hoisted into this struct the way the TS hoists it into the
// createDocPrFlow closure, so its rolling-window state survives across Run
// calls (INV-11).
type docPrFlow struct {
	deps       DocPrFlowDeps
	client     DocPrClient
	factory    *BranchFactory
	filter     PrFilter
	ledger     MergeLedgerStore
	burstClock *flowBurstClock

	burstMu                sync.Mutex
	burstLimiter           HourlyBurstLimiter
	burstLimiterMaxPerHour int
}

// assertFlowDeps is the fail-closed deps validation (a wiring bug, fail loud).
// The TS typeof-function probes are unreachable on Go's typed fields — a nil
// pointer / nil interface / nil func IS the malformed state, so those are
// asserted here.
func assertFlowDeps(deps DocPrFlowDeps) error {
	if deps.Git == nil {
		return docPrFlowConfigError(
			"createDocPrFlow deps.git must be a GitClient (diffFiles/rebaseBranch/statusPorcelain required)")
	}
	if deps.Platform == nil {
		return docPrFlowConfigError(
			"createDocPrFlow deps.platform must be a PrPlatformPort (listOpenDocsPrs required)")
	}
	if len(deps.Repo) == 0 {
		return docPrFlowConfigError("createDocPrFlow deps.repo must be a nonempty string")
	}
	if len(deps.DefaultBranch) == 0 {
		return docPrFlowConfigError("createDocPrFlow deps.defaultBranch must be a nonempty string")
	}
	if deps.Store == nil {
		return docPrFlowConfigError(
			"createDocPrFlow deps.store must be a StateStore (writeMergeLedger required)")
	}
	if deps.Incidents == nil {
		return docPrFlowConfigError(
			"createDocPrFlow deps.incidents must be an IncidentLog (record required)")
	}
	if deps.Clock == nil {
		return docPrFlowConfigError("createDocPrFlow deps.clock must be a () => string function")
	}
	// deps.Reverify is OPTIONAL (nil = absent) and deps.Delay is OPTIONAL (nil =
	// a real sleep); neither has a "defined but not a function" Go state.
	if deps.Apply == nil {
		return docPrFlowConfigError(
			"createDocPrFlow deps.apply must be a disk seam ((path, content) => Promise<void>)")
	}
	return nil
}

// assertFlowInput is the fail-closed per-run input validation. The TS
// non-object / non-array probes are unreachable on Go's typed fields; the
// meaningful content checks (nonempty run id, parsed manifest, nonempty
// optional strings) are ported here.
func assertFlowInput(input *DocPrFlowInput) error {
	if len(input.RunID) == 0 {
		return docPrFlowInputError("input.runId must be a nonempty string")
	}
	if input.Manifest == nil || input.Manifest.Documents == nil {
		return docPrFlowInputError("input.manifest must be a parsed Manifest")
	}
	if input.RunHeadSha != nil && len(*input.RunHeadSha) == 0 {
		return docPrFlowInputError("input.runHeadSha must be a nonempty string when provided")
	}
	if input.Provenance != nil && len(input.Provenance.RunID) == 0 {
		return docPrFlowInputError("input.provenance.runId must be a nonempty string")
	}
	if input.ProvenanceRecord != nil && len(input.ProvenanceRecord.RunID) == 0 {
		return docPrFlowInputError(
			"input.provenanceRecord must be a docsync.provenance/v1 record or null")
	}
	return nil
}

// CreateDocPrFlow builds the composed doc-PR flow (P6-T25/T26/T28).
// Deterministic orchestrator code end to end: no model calls, no wall-clock
// reads, no credentials retained (INV-8 via the composed client), no path
// outside the plan. The credential union is validated NOW (fail closed at
// composition, INV-8); the client object retains nothing.
func CreateDocPrFlow(deps DocPrFlowDeps) (DocPrFlow, error) {
	if err := assertFlowDeps(deps); err != nil {
		return nil, err
	}
	client, err := NewDocPrClient(DocPrClientDeps{
		Platform:      deps.Platform,
		Credentials:   deps.Credentials,
		Repo:          deps.Repo,
		DefaultBranch: deps.DefaultBranch,
		Clock:         deps.Clock,
	})
	if err != nil {
		return nil, err
	}
	filter, err := CreatePrFilter(PrFilterDeps{Git: deps.Git})
	if err != nil {
		return nil, err
	}
	ledger, err := NewMergeLedgerStore(MergeLedgerStoreDeps{Store: deps.Store})
	if err != nil {
		return nil, err
	}
	return &docPrFlow{
		deps:       deps,
		client:     client,
		factory:    CreateBranchFactory(deps.Git),
		filter:     filter,
		ledger:     ledger,
		burstClock: &flowBurstClock{clock: deps.Clock},
	}, nil
}

// limiterFor returns the ONE closure-hoisted burst limiter, creating it on
// first use (M1). A later run demanding a DIFFERENT maxPerHour is a typed
// config refusal: silently resetting the ring would be a flood risk (INV-11).
func (f *docPrFlow) limiterFor(maxPerHour int) (HourlyBurstLimiter, error) {
	f.burstMu.Lock()
	defer f.burstMu.Unlock()
	if f.burstLimiter == nil {
		limiter, err := NewHourlyBurstLimiter(
			BurstLimiterConfig{MaxPerHour: maxPerHour}, f.burstClock, f.deps.Delay)
		if err != nil {
			return nil, err
		}
		f.burstLimiter = limiter
		f.burstLimiterMaxPerHour = maxPerHour
		return limiter, nil
	}
	if f.burstLimiterMaxPerHour != maxPerHour {
		return nil, docPrFlowConfigError(
			"doc-PR flow burst budget changed between runs (" +
				strconv.Itoa(f.burstLimiterMaxPerHour) + " → " + strconv.Itoa(maxPerHour) +
				") — the hoisted burst limiter's rolling-window state would silently reset " +
				"(INV-11); one flow instance carries ONE burst budget")
	}
	return f.burstLimiter, nil
}

// buildFlowCandidates is the partial-pass candidate view of the frozen outcome
// (pure; P6-T10/D4): every auto/suggest document, with `verified` as the gate.
// ExcludedPaths removes docs whose patches were dropped as no-ops (M3b) so the
// report never lists a doc that does not ride the PR.
func buildFlowCandidates(outcome schemas.RunOutcome, excludedPaths map[string]bool) []PartialPassCandidate {
	candidates := make([]PartialPassCandidate, 0, len(outcome.Documents))
	for index := range outcome.Documents {
		doc := &outcome.Documents[index]
		if doc.PolicyOutcome != schemas.PipelineOutcomeAuto &&
			doc.PolicyOutcome != schemas.PipelineOutcomeSuggest {
			continue
		}
		if excludedPaths[doc.DocPath] {
			continue
		}
		candidate := PartialPassCandidate{
			DocPath:  doc.DocPath,
			Verified: doc.Verification != nil && doc.Verification.Verdict == schemas.VerdictPass,
			// The full VerificationReport is state-resident evidence; the flow
			// holds only the outcome's EMBEDDED verification summary and never
			// fabricates a report — the summary rides the drop `detail` instead.
			Verification: nil,
		}
		if doc.Verification != nil {
			candidate.Detail =
				"run-outcome verification verdict '" + string(doc.Verification.Verdict) +
					"' (auto_eligible: " + strconv.FormatBool(doc.Verification.AutoEligible) +
					") — embedded outcome record is the evidence; the full report lives in .docsync/state"
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

// renderFlowBody is the PR body = canonical report render + the D5 provenance
// record block + the partial-pass section (deterministic).
func renderFlowBody(
	outcome schemas.RunOutcome,
	canonical schemas.CanonicalReport,
	sourcePrNumber int,
	partialPass PartialPassResult,
	provenanceRecord *schemas.Provenance,
) string {
	report := renderPrBodySafe(outcome, canonical, sourcePrNumber)
	sections := []string{strings.TrimRightFunc(report, unicode.IsSpace)}
	// D5 canonical provenance block (P9-T06): the docsync.provenance/v1 record
	// rendered for review — PR-body markdown ONLY, never a doc file (D5 rule 5).
	if provenanceRecord != nil {
		block := strings.TrimRightFunc(provenance.RenderProvenanceSummary(*provenanceRecord), unicode.IsSpace)
		sections = append(sections, block)
	}
	sections = append(sections, RenderPartialPassReport(partialPass))
	return strings.Join(sections, "\n\n")
}

// renderPrBodySafe renders the run-level PR body (RenderPrBody): SourcePR is 0
// (absent) when the run has no source PR.
func renderPrBodySafe(
	outcome schemas.RunOutcome,
	canonical schemas.CanonicalReport,
	sourcePrNumber int,
) string {
	return RenderPrBody(RenderPrBodyInput{
		Outcome:  outcome,
		Report:   canonical,
		SourcePR: sourcePrNumber,
	})
}

// Run composes the gated steps in the documented order. It either resolves to a
// PrFlowResult (every semantic outcome) or returns a typed error (a malformed
// input/deps, an internal-invariant violation, or an unclassifiable git/platform
// failure — the Go projection of the TS throws).
func (f *docPrFlow) Run(ctx context.Context, input DocPrFlowInput) (PrFlowResult, error) {
	if err := assertFlowInput(&input); err != nil {
		return PrFlowResult{}, err
	}

	deps := f.deps
	git := deps.Git
	platform := deps.Platform
	repo := deps.Repo
	defaultBranch := deps.DefaultBranch
	clock := deps.Clock
	sourcePr := input.SourcePr

	sourcePrNumber := 0
	var sourcePrNumberPtr *int
	if sourcePr != nil {
		sourcePrNumber = sourcePr.Number
		number := sourcePr.Number
		sourcePrNumberPtr = &number
	}

	// Runtime trace (m4): mark records ONE port call under a deterministic seam.
	trace := []PrFlowTraceEntry{}
	mark := func(seam PrFlowTraceSeam, op string) {
		trace = append(trace, PrFlowTraceEntry{Seam: seam, Op: op})
	}
	aborted := func(reason, detail string, violations []PrFlowViolation) PrFlowResult {
		return PrFlowResult{Kind: PrFlowKindAborted, Reason: reason, Detail: detail, Violations: violations, Trace: trace}
	}
	// incident is the single refusal→incident choke point (OD-R50): a refused
	// path that targets an authority-config surface is classified as model
	// influence on the system's permissions/file selection/credentials.
	incident := func(rule, offendingPath, detail string, docPath *string) error {
		mark(PrFlowTraceIncidents, "record")
		recordRule := schemas.SafetyIncidentRule(rule)
		recordDetail := detail
		if IsAuthorityConfigSurface(offendingPath) {
			recordRule = schemas.SafetyIncidentRuleModelInfluenceOnAuthority
			recordDetail = "authority-config surface: " + detail
		}
		_, recErr := deps.Incidents.Record(ctx, RecordIncidentInput{
			At:             deps.Clock(),
			RunID:          input.RunID,
			Rule:           recordRule,
			OffendingPath:  offendingPath,
			Detail:         recordDetail,
			DocPath:        docPath,
			SourcePRNumber: sourcePrNumberPtr,
		})
		return recErr
	}

	// ---- (0) DIRTY-WORKTREE PRE-FLIGHT (M5) — before ANY ref creation.
	// A dirty tree is never branched off, never committed to, never pushed.
	mark(PrFlowTraceGit, "statusPorcelain")
	dirty, spErr := git.StatusPorcelain(ctx)
	if spErr != nil {
		return PrFlowResult{}, spErr
	}
	if len(dirty) > 0 {
		return aborted(string(PrFlowAbortDirtyWorktree),
			"working tree is not clean ("+strconv.Itoa(len(dirty))+
				" status entr(y/ies), first: "+gateJSONString(dirty[0])+
				") — the flow refuses to create refs or ship from a dirty worktree "+
				"(fail closed; commit or stash first)",
			nil), nil
	}

	// Sanity: a provenance record must name THIS run (incoherent input is a
	// caller bug, not a safety incident — no incident record).
	if input.Provenance != nil && input.Provenance.RunID != input.RunID {
		return aborted(string(PrFlowAbortInconsistentInput),
			"input.provenance.runId '"+input.Provenance.RunID+"' does not match the flow "+
				"runId '"+input.RunID+"' — a provenance record naming another run is "+
				"incoherent (fail closed)",
			nil), nil
	}
	provenanceRecord := input.ProvenanceRecord
	if provenanceRecord != nil && provenanceRecord.RunID != input.RunID {
		return aborted(string(PrFlowAbortInconsistentInput),
			"input.provenanceRecord.run_id '"+provenanceRecord.RunID+"' does not match the "+
				"flow runId '"+input.RunID+"' — a provenance record naming another run is "+
				"incoherent (fail closed)",
			nil), nil
	}

	// ---- (1) PROVENANCE GATE + commit-plan assembly (P6-T25 / INV-10).
	// AssembleDocCommit asserts provenance presence FIRST; the plan it emits is
	// the ONLY thing the write path may touch. P9-T06: BOTH evidence forms are
	// required — the FlowProvenance run evidence AND the docsync.provenance/v1
	// record. Either one missing routes through the SAME ProvenanceMissingError
	// refusal below (incident + abort, before ANY git or platform call).
	var provenanceRecordValue any
	if input.Provenance != nil && provenanceRecord != nil {
		provenanceRecordValue = provenanceRecord
	}
	provenanceRunID := input.RunID
	if provenanceRecord != nil {
		provenanceRunID = provenanceRecord.RunID
	}
	provenanceRef := ProvenanceRef{Record: provenanceRecordValue, RunID: provenanceRunID}
	plan, err := AssembleDocCommit(AssembleDocCommitInput{
		Patches:      input.Patches,
		StateUpdates: input.StateUpdates,
		Provenance:   provenanceRef,
		DocContents:  input.DocContents,
	})
	if err != nil {
		var provErr *ProvenanceMissingError
		if errors.As(err, &provErr) {
			offendingPath := "(run)"
			if len(input.Patches) > 0 {
				offendingPath = input.Patches[0].DocPath
			}
			if ierr := incident("provenance_missing", offendingPath,
				"flow aborted before any git or platform call: the run carries no provenance "+
					"(input.provenance / input.provenanceRecord) — INV-10: every automated change "+
					"carries its evidence record; a provenance-less PR is a bug, not a format "+
					"option (P6-T25/P9-T06 gate)",
				nil); ierr != nil {
				return PrFlowResult{}, ierr
			}
			return aborted(string(PrFlowAbortProvenanceMissing), provErr.Error(), nil), nil
		}
		var asmErr *CommitAssemblyError
		if errors.As(err, &asmErr) {
			return aborted(string(PrFlowAbortInconsistentInput),
				"commit-plan assembly refused: "+asmErr.Error(), nil), nil
		}
		return PrFlowResult{}, err
	}

	// ---- (2) CONTENT GUARD (P6-T27 / INV-7) — still before ANY side effect.
	// A DocPatch is content-anchored and cannot express a mode change, so no
	// MachinePrDocPatch asserts ModeAfter; the guard's mode rule is
	// redundant-in-depth with the pipeline's D2 gate.
	changedPaths := make([]string, len(plan.Files))
	for i, file := range plan.Files {
		changedPaths[i] = file.Path
	}
	manifestPaths := make([]string, len(input.Manifest.Documents))
	for i, doc := range input.Manifest.Documents {
		manifestPaths[i] = doc.Path
	}
	docPatches := make([]MachinePrDocPatch, len(input.Patches))
	for i, patch := range input.Patches {
		docPatches[i] = MachinePrDocPatch{DocPath: patch.DocPath}
	}
	guardResult, err := GuardMachinePrContent(GuardMachinePrContentInput{
		ChangedPaths:  changedPaths,
		ManifestPaths: manifestPaths,
		DocPatches:    docPatches,
	})
	if err != nil {
		return PrFlowResult{}, err
	}
	if !guardResult.OK {
		for _, violation := range guardResult.Violations {
			var docPath *string
			if violation.Rule == MachinePrRuleModeTransition {
				path := violation.Path
				docPath = &path
			}
			if ierr := incident(string(violation.Rule), violation.Path, violation.Detail, docPath); ierr != nil {
				return PrFlowResult{}, ierr
			}
		}
		guardViolations := make([]PrFlowViolation, 0, len(guardResult.Violations))
		for _, violation := range guardResult.Violations {
			guardViolations = append(guardViolations, PrFlowViolation{
				Path: violation.Path, Rule: string(violation.Rule), Detail: violation.Detail})
		}
		return aborted(string(PrFlowAbortGuardViolation),
			"machine-PR content guard refused the whole change set ("+
				strconv.Itoa(len(guardResult.Violations))+" violation(s)) — INV-7: machine PRs "+
				"never edit manifest/policy files and never transition a mode to auto (the "+
				"only path to auto is a human PR)",
			guardViolations), nil
	}

	// ---- (3) PARTIAL-PASS CLASSIFICATION (P6-T10/D4) — pure. Every patch must
	// map to an outcome document (an evidence gap is a caller bug, fail closed);
	// a patch whose document did NOT pass verification is DROPPED in-flow.
	outcomeDocPaths := make(map[string]bool, len(input.Outcome.Documents))
	for _, doc := range input.Outcome.Documents {
		outcomeDocPaths[doc.DocPath] = true
	}
	for _, patch := range input.Patches {
		if !outcomeDocPaths[patch.DocPath] {
			return aborted(string(PrFlowAbortInconsistentInput),
				"patch doc_path '"+patch.DocPath+"' does not appear in the frozen outcome's "+
					"documents — the flow consumes pre-derived evidence and never invents a "+
					"row (fail closed)",
				nil), nil
		}
	}
	survivors0 := make([]schemas.DocPatch, 0, len(input.Patches))
	for _, patch := range input.Patches {
		for _, doc := range input.Outcome.Documents {
			if doc.DocPath != patch.DocPath {
				continue
			}
			if (doc.PolicyOutcome == schemas.PipelineOutcomeAuto ||
				doc.PolicyOutcome == schemas.PipelineOutcomeSuggest) &&
				doc.Verification != nil && doc.Verification.Verdict == schemas.VerdictPass {
				survivors0 = append(survivors0, patch)
			}
			break
		}
	}
	if len(survivors0) == 0 {
		// Nothing verification-passing to ship — no PR, no platform call, no
		// commit (PLAN §12.3: no PR noise).
		partialPassEmpty, pErr := AssemblePartialPass(buildFlowCandidates(input.Outcome, nil))
		if pErr != nil {
			return PrFlowResult{}, pErr
		}
		return PrFlowResult{
			Kind:         PrFlowKindNoOp,
			DroppedNoOps: []PrFlowNoOpDrop{},
			PartialPass:  &partialPassEmpty,
			Trace:        trace,
		}, nil
	}
	if len(survivors0) != len(input.Patches) {
		plan, err = AssembleDocCommit(AssembleDocCommitInput{
			Patches:      survivors0,
			StateUpdates: input.StateUpdates,
			Provenance:   provenanceRef,
			DocContents:  input.DocContents,
		})
		if err != nil {
			return PrFlowResult{}, err
		}
	}

	// ---- (4) BRANCH + COMMIT (INV-1 guard; the filter moved below the M3 block).
	var headSha string
	if sourcePr != nil {
		headSha = sourcePr.HeadSha
	} else {
		mark(PrFlowTraceGit, "resolveRev")
		resolved, rerr := git.ResolveRev(ctx, "HEAD")
		if rerr != nil {
			return PrFlowResult{}, rerr
		}
		headSha = resolved
	}
	var baseBranchName string
	if sourcePr != nil {
		name, nerr := f.factory.BranchNameForSourcePr(sourcePr.Number, headSha)
		if nerr != nil {
			return PrFlowResult{}, nerr
		}
		baseBranchName = name
	} else {
		name, nerr := f.factory.BranchNameForRun(input.RunID)
		if nerr != nil {
			return PrFlowResult{}, nerr
		}
		baseBranchName = name
	}
	if err := AssertNotDefaultBranch(baseBranchName, defaultBranch); err != nil {
		return PrFlowResult{}, err
	}

	// Reuse a prior run's branch when its tip descends from the run head; the
	// factory's -2 disambiguator handles foreign collisions.
	branch := baseBranchName
	branchCreated := false
	mark(PrFlowTraceGit, "resolveRev")
	existingTip, existingExists, btErr := branchTip(ctx, git, baseBranchName)
	if btErr != nil {
		return PrFlowResult{}, btErr
	}
	reused := false
	if existingExists {
		mark(PrFlowTraceGit, "mergeBase")
		merged, mErr := git.MergeBase(ctx, headSha, existingTip)
		if mErr != nil {
			return PrFlowResult{}, mErr
		}
		if merged == headSha {
			reused = true
		}
	}
	if !reused {
		mark(PrFlowTraceGit, "createDocBranch")
		var sourcePrPtr *int
		if sourcePr != nil {
			number := sourcePr.Number
			sourcePrPtr = &number
		}
		created, cErr := f.factory.CreateDocBranch(ctx, CreateDocBranchInput{
			RepoRoot:      git.Root(),
			SourcePr:      sourcePrPtr,
			RunID:         input.RunID,
			HeadSha:       headSha,
			DefaultBranch: defaultBranch,
		})
		if cErr != nil {
			return PrFlowResult{}, cErr
		}
		branch = created.Branch
		branchCreated = created.Created
	}

	// Switch the worktree to the doc branch (the only typed branch-switching
	// primitive; a no-op replay — the branch tip always descends from the run
	// head). HEAD stays on the doc branch afterwards.
	mark(PrFlowTraceGit, "rebaseBranch")
	switchResult, swErr := git.RebaseBranch(ctx, branch, headSha)
	if swErr != nil {
		return PrFlowResult{}, swErr
	}
	if !switchResult.Ok {
		return PrFlowResult{}, docPrFlowConfigError(
			"rebase switch to doc branch '" + branch + "' conflicted — the composed flow " +
				"requires a no-op replay (the branch tip descends from the run head); a " +
				"conflict is a bug (fail loud)")
	}

	var commitSha *string
	mark(PrFlowTraceGit, "writeDocCommit")
	written, wErr := WriteDocCommit(ctx, git, plan, deps.Apply)
	if wErr != nil {
		var emptyErr *EmptyCommitError
		if !errors.As(wErr, &emptyErr) {
			return PrFlowResult{}, wErr
		}
		// Idempotent re-run: the branch already carries EXACTLY this content —
		// no new commit; the PR upsert below still updates the marker-keyed PR.
	} else {
		sha := written.Sha
		commitSha = &sha
	}

	// ---- (5) M3a: REAL REBASE onto the CURRENT default-branch head.
	mark(PrFlowTraceGit, "mergeBase")
	preRebaseMergeBase, mbErr := git.MergeBase(ctx, branch, defaultBranch)
	if mbErr != nil {
		return PrFlowResult{}, mbErr
	}
	mark(PrFlowTraceGit, "rebaseBranch")
	rebase, rbErr := git.RebaseBranch(ctx, branch, defaultBranch)
	if rbErr != nil {
		return PrFlowResult{}, rbErr
	}
	if !rebase.Ok {
		return aborted(string(PrFlowAbortRebaseConflict),
			"rebasing the doc branch '"+branch+"' onto '"+defaultBranch+"' conflicted — "+
				"the rebase was aborted by the git client (branch and tree restored "+
				"byte-identical), nothing is force-applied (A9: the conflict abstains to REVIEW)",
			nil), nil
	}
	if commitSha != nil {
		mark(PrFlowTraceGit, "resolveRev")
		tip, tErr := git.ResolveRev(ctx, branch)
		if tErr != nil {
			return PrFlowResult{}, tErr
		}
		commitSha = &tip
	}

	// ---- (6) M3b: NO-OP DROP — each surviving patch is re-evaluated against
	// the DEFAULT-branch head content (the world WITHOUT this flow's commit).
	droppedNoOps := []PrFlowNoOpDrop{}
	var survivors []schemas.DocPatch
	for i := range survivors0 {
		patch := &survivors0[i]
		mark(PrFlowTraceGit, "catFile")
		headContent := ""
		head, catErr := git.CatFile(ctx, defaultBranch, patch.DocPath)
		if catErr != nil {
			if !isMissingRev(catErr) {
				return PrFlowResult{}, catErr
			}
			// the document does not exist upstream — never a no-op.
		} else {
			headContent = head.Content
		}
		var baseContent *string
		if preRebaseMergeBase != "" {
			mark(PrFlowTraceGit, "catFile")
			candidate, cErr := git.CatFile(ctx, preRebaseMergeBase, patch.DocPath)
			if cErr != nil {
				if !isMissingRev(cErr) {
					return PrFlowResult{}, cErr
				}
			} else if docutil.ContentHash(candidate.Content) == patch.BaseContentHash {
				// Only a snapshot that hashes to the patch's own baseline is
				// honest; anything else stays OMITTED (raced head → abstain).
				content := candidate.Content
				baseContent = &content
			}
		}
		drop, dErr := EvaluateNoOpDrop(ctx, EvaluateNoOpDropInput{
			Patch:       patch,
			HeadContent: headContent,
			BaseContent: baseContent,
		})
		if dErr != nil {
			return PrFlowResult{}, dErr
		}
		if drop.Noop {
			droppedNoOps = append(droppedNoOps, PrFlowNoOpDrop{DocPath: patch.DocPath, Detail: drop.Detail})
		} else {
			survivors = append(survivors, *patch)
		}
	}
	if len(survivors) == 0 {
		// ALL patches dropped — no PR noise (PLAN §12.3): no platform call, the
		// branch stays for forensics (no cleanup, ever).
		excluded := make(map[string]bool, len(droppedNoOps))
		for _, dropNoOp := range droppedNoOps {
			excluded[dropNoOp.DocPath] = true
		}
		partialPassNoOp, pErr := AssemblePartialPass(buildFlowCandidates(input.Outcome, excluded))
		if pErr != nil {
			return PrFlowResult{}, pErr
		}
		return PrFlowResult{
			Kind:         PrFlowKindNoOp,
			Branch:       branch,
			DroppedNoOps: droppedNoOps,
			PartialPass:  &partialPassNoOp,
			Trace:        trace,
		}, nil
	}
	if len(droppedNoOps) > 0 {
		plan, err = AssembleDocCommit(AssembleDocCommitInput{
			Patches:      survivors,
			StateUpdates: input.StateUpdates,
			Provenance:   provenanceRef,
			DocContents:  input.DocContents,
		})
		if err != nil {
			return PrFlowResult{}, err
		}
	}

	// ---- (7) M3c: RE-VERIFY GATE (A9) — the default branch has ADVANCED past
	// the analysis anchor → the merged content must RE-PASS verification.
	var effectiveRunHeadSha *string
	if input.RunHeadSha != nil {
		anchor := *input.RunHeadSha
		effectiveRunHeadSha = &anchor
	} else if sourcePr == nil {
		effectiveRunHeadSha = &headSha
	}
	if effectiveRunHeadSha != nil {
		mark(PrFlowTraceGit, "resolveRev")
		currentDefaultHead, rvErr := git.ResolveRev(ctx, defaultBranch)
		if rvErr != nil {
			return PrFlowResult{}, rvErr
		}
		if currentDefaultHead != *effectiveRunHeadSha {
			if deps.Reverify == nil {
				return aborted(string(PrFlowAbortReverifyUnavailable),
					"the default branch '"+defaultBranch+"' advanced past the analysis anchor "+
						*effectiveRunHeadSha+" (now "+currentDefaultHead+") and no re-verify hook "+
						"is configured — the flow refuses to ship merged content it cannot "+
						"re-verify (fail closed, A9: never ships unverified)",
					nil), nil
			}
			for i := range survivors {
				patch := &survivors[i]
				mark(PrFlowTraceGit, "catFile")
				mergedContent := ""
				merged, mcErr := git.CatFile(ctx, branch, patch.DocPath)
				if mcErr != nil {
					if !isMissingRev(mcErr) {
						return PrFlowResult{}, mcErr
					}
					// the merged tree no longer carries the doc — re-verify fails below.
				} else {
					mergedContent = merged.Content
				}
				report := deps.Reverify(patch, mergedContent)
				verdict := ""
				autoEligible := false
				if report != nil {
					verdict = string(report.Verdict)
					autoEligible = report.AutoEligible
				}
				if verdict != string(schemas.VerdictPass) || !autoEligible {
					return aborted(string(PrFlowAbortReverifyFailed),
						"the merged content of doc '"+patch.DocPath+"' did not re-pass verification "+
							"after the rebase (verdict '"+verdict+"', auto_eligible "+
							strconv.FormatBool(autoEligible)+") — A9: abstains to REVIEW, never force-applies",
						nil), nil
				}
			}
		}
	}

	// ---- (8) PR FILTER (layer 4, INV-1/2/3/4) — on the ASSEMBLED, POST-REBASE
	// branch diff. Any violation aborts BEFORE any platform call; the branch
	// STAYS for forensics (no cleanup, ever).
	filterResult, fErr := f.filter.FilterBranchDiff(ctx, FilterBranchDiffInput{
		RepoRoot: git.Root(),
		Base:     defaultBranch,
		Branch:   branch,
		Manifest: input.Manifest,
	})
	if fErr != nil {
		return PrFlowResult{}, fErr
	}
	if !filterResult.OK {
		for _, violation := range filterResult.Violations {
			if ierr := incident(string(violation.Rule), violation.Path, violation.Detail, nil); ierr != nil {
				return PrFlowResult{}, ierr
			}
		}
		filterViolations := make([]PrFlowViolation, 0, len(filterResult.Violations))
		for _, violation := range filterResult.Violations {
			filterViolations = append(filterViolations, PrFlowViolation{
				Path: violation.Path, Rule: string(violation.Rule), Detail: violation.Detail})
		}
		return aborted(string(PrFlowAbortFilterViolation),
			"layer-4 PR filter refused the assembled branch diff of '"+branch+"' ("+
				strconv.Itoa(len(filterResult.Violations))+" violation(s)) — the branch remains "+
				"in place for forensics (incident evidence references committed content)",
			filterViolations), nil
	}

	// ---- (9) SELF-TRIGGER SUPPRESSION (P6-T19) — no incident, no platform call.
	var headBranch, headAuthor *string
	if sourcePr != nil {
		headBranch = sourcePr.HeadBranch
		headAuthor = sourcePr.Author
	}
	selfTrigger := EvaluateSelfTrigger(SelfTriggerCheckInput{
		Branch:         headBranch,
		AuthorIdentity: headAuthor,
		BotIdentity:    deps.OwnIdentity,
		DefaultBranch:  defaultBranch,
	})
	if selfTrigger.Suppressed {
		headBranchStr := "<unknown>"
		if headBranch != nil {
			headBranchStr = *headBranch
		}
		return aborted(string(PrFlowAbortSelfTriggered),
			"source PR head branch '"+headBranchStr+"' carries the '"+SelfTriggerBranchPrefix+
				"' prefix and its author matches docsync's own identity ("+selfTrigger.Reason+
				") — the system never creates a doc PR from its own doc PR (P6-T19; no "+
				"incident: loop prevention is not a safety violation)",
			nil), nil
	}

	// ---- (10) PARTIAL-PASS REPORT VIEW — docs whose patches were dropped as
	// no-ops are excluded so the report never lists a doc that does not ride.
	excludedNoOp := make(map[string]bool, len(droppedNoOps))
	for _, dropNoOp := range droppedNoOps {
		excludedNoOp[dropNoOp.DocPath] = true
	}
	partialPass, pErr := AssemblePartialPass(buildFlowCandidates(input.Outcome, excludedNoOp))
	if pErr != nil {
		return PrFlowResult{}, pErr
	}

	// ---- (11) IDEMPOTENCY KEY + UPSERT DECISION (M2/M4). The key covers the
	// run's FULL doc set (stable across re-runs): input.Patches, NOT survivors.
	docSetHash, dsErr := DocSetContentHash(input.Patches)
	if dsErr != nil {
		return PrFlowResult{}, dsErr
	}
	var sourceHeadSha *string
	if sourcePr != nil {
		sha := sourcePr.HeadSha
		sourceHeadSha = &sha
	}
	keyInput := ComputeIdempotencyKeyInput{SourcePrHeadSha: sourceHeadSha, DocSetContentHash: docSetHash}
	if sourcePr == nil {
		keyInput.RunID = input.RunID
	}
	idempotencyKey, ikErr := ComputeIdempotencyKey(keyInput)
	if ikErr != nil {
		return PrFlowResult{}, ikErr
	}
	marker := IdempotencyMarker(idempotencyKey)
	sourceKey := "local-" + input.RunID
	if sourcePr != nil {
		sourceKey = "pr-" + strconv.Itoa(sourcePr.Number)
	}
	mark(PrFlowTraceLedger, "load")
	ledgerNow, ldErr := f.ledger.Load(sourceKey)
	if ldErr != nil {
		return PrFlowResult{}, ldErr
	}
	var openEntry *schemas.LedgerEntry
	if ledgerNow != nil {
		for i := range ledgerNow.Entries {
			if ledgerNow.Entries[i].State == schemas.MergeLedgerStateOpen {
				entry := ledgerNow.Entries[i]
				openEntry = &entry
				break
			}
		}
	}
	mark(PrFlowTracePlatform, "findByPrBodyMarker")
	openSameKey, openSameFound, msErr := platform.FindByPrBodyMarker(ctx, repo, marker, &PrMarkerLookupOptions{State: PrLookupOpen})
	if msErr != nil {
		return PrFlowResult{}, msErr
	}
	var openPrior *PrRef
	openPriorKey := idempotencyKey
	if openSameFound {
		pr := openSameKey
		openPrior = &pr
	} else if openEntry != nil && openEntry.IdempotencyKey != idempotencyKey {
		// The ledger's open entry names a STALE analysis (different key): locate
		// that open PR by its own marker to supersede it (M2), never duplicate.
		mark(PrFlowTracePlatform, "findByPrBodyMarker")
		stalePr, staleFound, stErr := platform.FindByPrBodyMarker(ctx, repo, IdempotencyMarker(openEntry.IdempotencyKey), &PrMarkerLookupOptions{State: PrLookupOpen})
		if stErr != nil {
			return PrFlowResult{}, stErr
		}
		if staleFound {
			pr := stalePr
			openPrior = &pr
			openPriorKey = openEntry.IdempotencyKey
		}
	}
	var existingPrior *SupersedenceExistingPr
	if openPrior != nil {
		existingPrior = &SupersedenceExistingPr{PrNumber: openPrior.Number, IdempotencyKey: openPriorKey}
	}
	supersedeDecision, sdErr := EvaluateSupersedence(existingPrior, idempotencyKey)
	if sdErr != nil {
		return PrFlowResult{}, sdErr
	}

	// ---- (12) OPEN-PR CAP (P6-T06) — governs CREATION only: the update path
	// of a same-key prior SKIPS the cap; a downgrade never reaches create/update.
	if supersedeDecision.Action != SupersedenceActionUpdate {
		mark(PrFlowTracePlatform, "listOpenDocsPrs")
		openPrs, lErr := platform.ListOpenDocsPrs(ctx, repo)
		if lErr != nil {
			return PrFlowResult{}, lErr
		}
		maxOpen := 3
		if input.Budgets != nil && input.Budgets.MaxOpen != nil {
			maxOpen = *input.Budgets.MaxOpen
		}
		capGovernor, cgErr := NewOpenPrCapGovernor(OpenPrCapConfig{MaxOpen: maxOpen})
		if cgErr != nil {
			return PrFlowResult{}, cgErr
		}
		capResult, cpErr := capGovernor.Evaluate(len(openPrs))
		if cpErr != nil {
			return PrFlowResult{}, cpErr
		}
		if !capResult.Allowed && capResult.Downgrade {
			message := capResult.DowngradeMsg
			if len(message) == 0 {
				message = PatchReadyMessage
			}
			return PrFlowResult{
				Kind:      PrFlowKindDowngradedReportOnly,
				Reason:    string(PrFlowDowngradeOpenPrCapReached),
				Message:   message,
				OpenCount: len(openPrs),
				MaxOpen:   maxOpen,
				Branch:    branch,
				Trace:     trace,
			}, nil
		}
	}

	// ---- (13) HOURLY BURST (P6-T07) — admit (queue-not-fail) through the
	// CLOSURE-HOISTED limiter (one instance, one budget, INV-11).
	maxPerHour := 10
	if input.Budgets != nil && input.Budgets.MaxPerHour != nil {
		maxPerHour = *input.Budgets.MaxPerHour
	}
	limiter, limErr := f.limiterFor(maxPerHour)
	if limErr != nil {
		return PrFlowResult{}, limErr
	}
	if _, adErr := limiter.Admit(ctx); adErr != nil {
		return PrFlowResult{}, adErr
	}
	if clockErr := f.burstClock.Err(); clockErr != nil {
		return PrFlowResult{}, clockErr
	}

	// ---- (14) RE-CREATION BRANCH (M4): a CLOSED prior carrying the same marker
	// (merged/superseded/dropped) means the same doc set legitimately re-ships —
	// a NEW PR on a DISAMBIGUATED branch (the factory's deterministic -2 scan).
	prBranch := branch
	prBranchCreated := branchCreated
	if supersedeDecision.Action == SupersedenceActionCreate {
		mark(PrFlowTracePlatform, "findByPrBodyMarker")
		_, closedFound, cp2Err := platform.FindByPrBodyMarker(ctx, repo, marker, nil)
		if cp2Err != nil {
			return PrFlowResult{}, cp2Err
		}
		if closedFound {
			taken := map[string]bool{}
			for attempt := 0; attempt < MAX_DISAMBIGUATOR_CANDIDATES; attempt++ {
				candidate := branch
				if attempt > 0 {
					candidate = branch + "-" + strconv.Itoa(attempt+1)
				}
				mark(PrFlowTraceGit, "resolveRev")
				_, exists, bitErr := branchTip(ctx, git, candidate)
				if bitErr != nil {
					return PrFlowResult{}, bitErr
				}
				if !exists {
					break
				}
				taken[candidate] = true
			}
			rebranch, rbNameErr := DisambiguatedBranchName(branch, taken)
			if rbNameErr != nil {
				return PrFlowResult{}, rbNameErr
			}
			mark(PrFlowTraceGit, "resolveRev")
			rebranchPoint, rpErr := git.ResolveRev(ctx, branch)
			if rpErr != nil {
				return PrFlowResult{}, rpErr
			}
			mark(PrFlowTraceGit, "createBranch")
			startPoint := rebranchPoint
			if cbErr := git.CreateBranch(ctx, rebranch, &startPoint); cbErr != nil {
				return PrFlowResult{}, cbErr
			}
			mark(PrFlowTraceGit, "rebaseBranch")
			reSwitch, rsErr := git.RebaseBranch(ctx, rebranch, rebranchPoint)
			if rsErr != nil {
				return PrFlowResult{}, rsErr
			}
			if !reSwitch.Ok {
				return PrFlowResult{}, docPrFlowConfigError(
					"re-creation branch switch to '" + rebranch + "' conflicted — the branch " +
						"was created at the current doc-branch tip, so the replay must be a " +
						"no-op; a conflict is a bug (fail loud)")
			}
			prBranch = rebranch
			prBranchCreated = true
		}
	}

	// ---- (15) PARTIAL-PASS BODY + UPSERT (P6-T05/T04/T10). The client's marker
	// lookup is scoped to OPEN PRs: a closed prior carrying the same marker is
	// INVISIBLE to the upsert (never mutated in place) and the upsert creates.
	body := renderFlowBody(input.Outcome, input.Canonical, sourcePrNumber, partialPass, input.ProvenanceRecord)
	title := "docs: synchronize documentation (local run " + input.RunID + ")"
	if sourcePr != nil {
		title = "docs: synchronize documentation with PR #" + strconv.Itoa(sourcePr.Number)
	}
	mark(PrFlowTracePlatform, "upsertDocPr")
	lookupState := PrLookupOpen
	upsert, upErr := f.client.UpsertDocPr(ctx, UpsertDocPrInput{
		Branch:         prBranch,
		Title:          title,
		Body:           body,
		IdempotencyKey: idempotencyKey,
		LookupState:    &lookupState,
	})
	if upErr != nil {
		return PrFlowResult{}, upErr
	}

	// SUPERSEDE the stale open PR (M2): the exact marker comment names the newer
	// PR's number, which only exists after the create.
	if supersedeDecision.Action == SupersedenceActionSupersedeThenCrea && openPrior != nil {
		mark(PrFlowTracePlatform, "appendPrComment")
		supersedeMark, smErr := SupersedeMarker(upsert.PR.Number)
		if smErr != nil {
			return PrFlowResult{}, smErr
		}
		if acErr := platform.AppendPrComment(ctx, *openPrior, supersedeMark); acErr != nil {
			return PrFlowResult{}, acErr
		}
		mark(PrFlowTracePlatform, "markSuperseded")
		if msErr := platform.MarkSuperseded(ctx, *openPrior, upsert.PR); msErr != nil {
			return PrFlowResult{}, msErr
		}
	}

	// ---- (16) MERGE-ORDER LEDGER (P6-T18/T26; R15) — the CAS point.
	if supersedeDecision.Action == SupersedenceActionSupersedeThenCrea && openPrior != nil {
		var staleEntry *schemas.LedgerEntry
		if ledgerNow != nil {
			for i := range ledgerNow.Entries {
				if ledgerNow.Entries[i].DocPRNumber == openPrior.Number {
					entry := ledgerNow.Entries[i]
					staleEntry = &entry
					break
				}
			}
		}
		if staleEntry == nil || staleEntry.State != schemas.MergeLedgerStateSuperseded {
			if staleEntry != nil {
				if _, trErr := f.ledger.Transition(staleEntry.State, schemas.MergeLedgerStateSuperseded); trErr != nil {
					return PrFlowResult{}, trErr
				}
			}
			mark(PrFlowTraceLedger, "record")
			if _, recErr := f.ledger.Record(sourceKey, RecordLedgerEntryInput{
				DocPRNumber:    openPrior.Number,
				State:          schemas.MergeLedgerStateSuperseded,
				IdempotencyKey: openPriorKey,
				Detail: "doc PR #" + strconv.Itoa(openPrior.Number) + " superseded by #" +
					strconv.Itoa(upsert.PR.Number) + " — newer analysis (flow run " + input.RunID + ")",
			}, clock, nil); recErr != nil {
				return PrFlowResult{}, recErr
			}
		}
	}
	var priorEntry *schemas.LedgerEntry
	if ledgerNow != nil {
		for i := range ledgerNow.Entries {
			if ledgerNow.Entries[i].DocPRNumber == upsert.PR.Number {
				entry := ledgerNow.Entries[i]
				priorEntry = &entry
				break
			}
		}
	}
	var ledgerHash *string
	casConflict := false
	rerunRequired := false
	var ledgerDetail *string
	if priorEntry != nil && priorEntry.State == schemas.MergeLedgerStateOpen {
		// Idempotent re-run: the PR is already recorded open — re-recording
		// open→open is illegal lifecycle drift; skipping IS the idempotent act.
	} else {
		if priorEntry != nil {
			if _, trErr := f.ledger.Transition(priorEntry.State, schemas.MergeLedgerStateOpen); trErr != nil {
				return PrFlowResult{}, trErr
			}
		}
		mark(PrFlowTraceLedger, "record")
		recorded, recErr := f.ledger.Record(sourceKey, RecordLedgerEntryInput{
			DocPRNumber:    upsert.PR.Number,
			State:          schemas.MergeLedgerStateOpen,
			IdempotencyKey: idempotencyKey,
			Detail: "doc PR #" + strconv.Itoa(upsert.PR.Number) + " " + string(upsert.Action) +
				" by flow run " + input.RunID,
		}, clock, nil)
		if recErr != nil {
			return PrFlowResult{}, recErr
		}
		if recorded.OK {
			hash := recorded.Hash
			ledgerHash = &hash
		} else {
			// R15: the loser NEVER overwrites the winner — the PR stands, the
			// result is annotated and the CALLER re-runs on a fresh read.
			casConflict = true
			rerunRequired = true
			detail := recorded.Detail
			ledgerDetail = &detail
		}
	}

	return PrFlowResult{
		Kind:           PrFlowResultKind(string(upsert.Action)),
		PR:             &upsert.PR,
		Branch:         prBranch,
		BranchCreated:  prBranchCreated,
		IdempotencyKey: idempotencyKey,
		CommitSha:      commitSha,
		PartialPass:    &partialPass,
		DroppedNoOps:   droppedNoOps,
		LedgerHash:     ledgerHash,
		CasConflict:    casConflict,
		RerunRequired:  rerunRequired,
		LedgerDetail:   ledgerDetail,
		Trace:          trace,
	}, nil
}
