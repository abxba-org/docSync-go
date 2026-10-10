// Ported from core/src/audit/compose.ts (P11-T21; ARCH §7.2 Loop B — Background
// drift audit, §10.2). DETERMINISTIC ORCHESTRATION, READ-MOSTLY: this module
// wires the P11-T18 trigger contract, the P11-T19 incremental audit engine,
// the P11-T20 findings store + advisory artifacts, and the P11-T17
// claim-support sweep into ONE composed entry — and it performs NO document
// writes and NO patch application ever (INV: advisory surface only; findings
// are DATA).
//
// Pipeline (per run):
//  1. ResolveLoopBTriggers (P11-T18) — an ineligible context (a PR event, ANY
//     PR number, an unconfigured schedule, a non-default branch) yields a
//     TYPED NO-OP result and NOTHING else happens: no git reads, no store
//     writes, no artifacts (V-P11-8 structural refusal, propagated
//     end-to-end).
//  2. Manifest gate — the caller-supplied manifest is re-validated against the
//     registered manifest schema (fail closed, D7/D8): an invalid manifest
//     produces a typed `abstained` result and NEVER a partial scan.
//  3. Position port — HEAD sha via the read-only git client.
//  4. AuditDocPort — the CLAIM-SUPPORT predicates (P11-T17): per manifest
//     doc, the claims anchored to that doc are collected from the claim store
//     and evaluated through RunClaimSupportSweep in REPORT-ONLY mode (the
//     drift module's per-claim logic is REUSED — the anchor + blob-identity
//     predicates are never duplicated here). Verdicts map onto the closed
//     drift-kind vocabulary (`claim-lost-support`, `claim-unanchored`;
//     `doc-drift`/`symbol-absent` stay reserved for the differential
//     producer).
//  5. RunIncrementalAudit (P11-T19) — chunked, cursor-resumed, within-run
//     claim-ID dedup, CAS-guarded cursor advance (the cursor port is wired
//     straight onto the real StateStore ReadAuditCursor/WriteAuditCursor pair
//     — CAS discipline lives inside the store, R15).
//  6. Cross-run dedup — DedupeAgainstStore against the persisted findings
//     view; only non-redundant findings are appended (ARCH §7.2 cost control:
//     nightly runs must not spam).
//  7. Run-provenance record — BuildDriftAuditRunRecord + append (D5:
//     provenance or it didn't happen).
//  8. Advisory artifacts — RenderDriftCheckRun + RenderDriftIssue (P11-T20),
//     optional via flag, default ON. They are returned as DATA; nothing is
//     POSTed here.
//
// Remediation NEVER happens here: findings that warrant patches escalate only
// through the SUGGEST path (suggest.go) — the same verification + policy
// engine as Loop A, one code path, two triggers (ARCH §7.2).
package audit

import (
	"context"
	"fmt"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/drift"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

/* ------------------------------------------------------------------- shapes */

// LoopBAuditKind discriminates the composed-audit result (the TS union tag).
type LoopBAuditKind string

// The composed-audit outcome kinds.
const (
	// LoopBAuditRan — the audit ran (status inside Result).
	LoopBAuditRan LoopBAuditKind = "ran"
	// LoopBAuditNoop — trigger-refused (nothing ran, nothing wrote).
	LoopBAuditNoop LoopBAuditKind = "noop"
	// LoopBAuditAbstained — fail-closed abstention BEFORE the engine (D14).
	LoopBAuditAbstained LoopBAuditKind = "abstained"
)

// LoopBAuditComposeResult is the composed Loop B audit result: a trigger-
// refused no-op, a fail-closed abstention, or a completed run. Exactly one of
// Reason / Detail / (Result, Findings, RunRecord) is populated, matching the
// TS discriminated union.
type LoopBAuditComposeResult struct {
	Kind LoopBAuditKind
	// Reason is the scheduler's own refusal vocabulary (noop).
	Reason LoopBNoopReason
	// Detail is the abstention detail (abstained).
	Detail string
	// Trigger is the scheduler verdict trigger that ran (ran).
	Trigger LoopBScheduledTrigger
	// Result is the engine's run result (ran).
	Result *AuditRunResult
	// Findings are this run's deduplicated findings (ran; empty otherwise).
	Findings []DriftFinding
	// RunRecord is the persisted run-provenance record (ran).
	RunRecord *schemas.DriftAuditRunRecord
	// Artifacts are the advisory artifacts (ran; nil when rendering was
	// disabled or nothing was rendered).
	Artifacts *LoopBAuditArtifacts
	// Appended is what the findings store persisted this run (cross-run
	// deduped; ran).
	Appended *AuditFindingsAppendResult
}

// LoopBAuditArtifacts are the advisory artifacts the composed audit returns as
// DATA (P11-T20) — nothing is posted here.
type LoopBAuditArtifacts struct {
	CheckRun CheckSummaryPayload
	Issue    string
}

// LoopBAuditComposeDeps are the injectable dependencies of RunLoopBAudit
// (wiring, W5b2 surface).
type LoopBAuditComposeDeps struct {
	// Config is the Loop B schedule configuration (P11-T18).
	Config LoopBScheduleConfig
	// Context is the trigger context (P11-T18).
	Context LoopBTriggerContext
	// Manifest is the caller-supplied (previously validated) manifest.
	// Re-validated here fail-closed before anything runs (D7/D8) — an invalid
	// manifest abstains, never a partial scan.
	Manifest schemas.Manifest
	// StateStore is the real CAS state store — the audit-cursor port source
	// (ARCH §6.3).
	StateStore *state.StateStore
	// Git is the read-only git access: HEAD position + the claim-support
	// sweep's reads.
	Git *gitx.GitClient
	// Claims is the claim store (P11-T17 sweep inputs, per-doc anchor
	// scoping).
	Claims *claims.ClaimStore
	// Store is the append-only findings/run-record store (P11-T20).
	Store AuditFindingsStore
	// RunID is the caller-assigned run id (deterministic caller; never
	// generated here).
	RunID string
	// Now is the ISO-8601 timestamp from the caller's injected clock (no
	// hidden clock).
	Now string
	// CommitWalkDepth is the bounded commit-walk depth for sweep commit
	// evidence (default 50).
	CommitWalkDepth *int
	// Concurrency is the engine worker-pool cap (P11-T19; default 4).
	Concurrency *int
	// RenderArtifacts renders the advisory check-run + issue artifacts
	// (default true).
	RenderArtifacts *bool
}

/* ------------------------------------------------------------------ helpers */

// abstained is the fail-closed pre-engine degradation (D14).
func abstainedResult(detail string) LoopBAuditComposeResult {
	return LoopBAuditComposeResult{
		Kind:      LoopBAuditAbstained,
		Detail:    detail,
		Findings:  []DriftFinding{},
		Artifacts: nil,
	}
}

// noopResult is the trigger-refused no-op (V-P11-8): NOTHING ran, NOTHING
// wrote.
func noopResult(reason LoopBNoopReason) LoopBAuditComposeResult {
	return LoopBAuditComposeResult{
		Kind:      LoopBAuditNoop,
		Reason:    reason,
		Findings:  []DriftFinding{},
		Artifacts: nil,
	}
}

// findingsFromReport maps one claim-support sweep verdict onto the closed
// drift-kind vocabulary. `supported` produces NO finding; the sweep is a
// state-at-HEAD query, so the audit cursor plays no role in the predicate
// (cross-run repeats collapse via the claim-ID dedup instead — ARCH §7.2).
func findingsFromReport(report *schemas.ClaimSupportReport, docPath, now string) []DriftFinding {
	findings := []DriftFinding{}
	for _, entry := range report.Claims {
		if entry.Verdict == schemas.ClaimSupportSupported {
			continue
		}
		kind := "claim-unanchored"
		if entry.Verdict == schemas.ClaimSupportLostSupport {
			kind = "claim-lost-support"
		}
		claimID := entry.ClaimID
		findings = append(findings, DriftFinding{
			ID:      "loopb:" + kind + ":" + entry.ClaimID,
			ClaimID: &claimID,
			DocPath: docPath,
			Kind:    kind,
			Evidence: DriftFindingEvidence{
				Commits: copyStrings(entry.Evidence.Commits),
				Paths:   uniqueSorted([]string{entry.Evidence.SourceFile, entry.Evidence.AnchorFile}),
				Detail:  entry.Reason,
			},
			CreatedAt: now,
		})
	}
	return findings
}

/* ------------------------------------------------------------- the compose */

// RunLoopBAudit runs ONE composed Loop B audit (see the module doc for the
// pipeline and the read-mostly/advisory invariants). Ineligible triggers and
// pre-engine failures return typed results — the trigger-refusal path NEVER
// throws and NEVER writes. Post-engine store failures surface as errors (fail
// loud: provenance is never dropped silently).
func RunLoopBAudit(ctx context.Context, deps LoopBAuditComposeDeps) (LoopBAuditComposeResult, error) {
	/* 1. Trigger contract (P11-T18): the structural PR refusal propagates
	 * end-to-end as a typed no-op — no audit, no store writes, no artifacts. */
	verdict, err := ResolveLoopBTriggers(deps.Config, deps.Context)
	if err != nil {
		return LoopBAuditComposeResult{}, err
	}
	if !verdict.Eligible {
		return noopResult(verdict.Reason), nil
	}

	/* 2. Manifest gate (D7/D8): re-validate the caller-supplied manifest —
	 * fail closed BEFORE anything runs; never a partial scan. */
	if issues := deps.Manifest.Validate(); len(issues) > 0 {
		return abstainedResult(
			"manifest failed re-validation — refusing to audit (fail closed, D7/D8; abstain, never a partial scan): " +
				firstIssueMessage(issues)), nil
	}
	if !schemas.IsSupportedManifestVersion(deps.Manifest.Version) {
		return abstainedResult(fmt.Sprintf(
			"manifest version %d is not supported — refusing to audit (fail closed, D8)",
			deps.Manifest.Version)), nil
	}

	/* 3. Position: HEAD sha via the read-only git client. */
	headSha, err := deps.Git.ResolveRev(ctx, "HEAD")
	if err != nil {
		return abstainedResult(
			"audit position resolution failed — refusing to audit (fail closed): " + describeError(err)), nil
	}

	/* Cursor-before snapshot for the run record (best effort — the engine
	 * re-reads through its own port and abstains typed on failure). */
	var cursorBefore *string
	if read, readErr := deps.StateStore.ReadAuditCursor(); readErr == nil && read.Exists && read.Content != nil {
		position := read.Content.Position
		cursorBefore = &position
	}

	/* 4. Engine ports, wired read-only. */
	cursor := stateStoreCursorPort{store: deps.StateStore}
	manifestDocs := func(context.Context) ([]string, error) {
		paths := make([]string, 0, len(deps.Manifest.Documents))
		for _, entry := range deps.Manifest.Documents {
			paths = append(paths, entry.Path)
		}
		return paths, nil
	}
	position := func(context.Context) (string, error) { return headSha, nil }
	auditDoc := func(ctx context.Context, docPath string, _ *schemas.AuditCursor) ([]DriftFinding, error) {
		// Claim-support predicates (P11-T17), REPORT-ONLY: the drift module's
		// per-claim logic is REUSED — the anchor + blob-identity predicates
		// are never duplicated here. A store/sweep failure is a CHUNK failure
		// inside the engine (typed partial), never a silent skip (D14).
		allClaims, listErr := deps.Claims.List()
		if listErr != nil {
			return nil, listErr
		}
		docClaims := make([]schemas.Claim, 0, len(allClaims))
		for _, claim := range allClaims {
			if claim.Anchor.File == docPath {
				docClaims = append(docClaims, claim)
			}
		}
		if len(docClaims) == 0 {
			return []DriftFinding{}, nil
		}
		sweep := drift.ClaimSupportSweepInput{
			Claims:          docClaims,
			Git:             deps.Git,
			Head:            headSha,
			RunID:           deps.RunID,
			At:              deps.Now,
			CommitWalkDepth: deps.CommitWalkDepth,
		}
		report, sweepErr := drift.RunClaimSupportSweep(ctx, sweep)
		if sweepErr != nil {
			return nil, sweepErr
		}
		return findingsFromReport(report, docPath, deps.Now), nil
	}

	/* 5. The incremental engine (P11-T19): chunked, cursor-resumed, CAS. */
	result := RunIncrementalAudit(ctx, IncrementalAuditDeps{
		Cursor:       cursor,
		ManifestDocs: manifestDocs,
		Position:     position,
		AuditDoc:     auditDoc,
	}, AuditRunOptions{Now: deps.Now, Concurrency: deps.Concurrency})

	/* 6. Cross-run dedup (P11-T20): append ONLY non-redundant findings. */
	prior, err := deps.Store.ListFindings(nil)
	if err != nil {
		return LoopBAuditComposeResult{}, err
	}
	dedup := DedupeAgainstStore(result.Findings, prior)
	appended, err := deps.Store.AppendFindings(dedup.Appendable)
	if err != nil {
		return LoopBAuditComposeResult{}, err
	}

	/* 7. Run-provenance record (D5). */
	runRecord, err := BuildDriftAuditRunRecord(BuildDriftAuditRunRecordInput{
		RunID:        deps.RunID,
		At:           deps.Now,
		Trigger:      schemas.DriftAuditTrigger(verdict.Trigger),
		Position:     headSha,
		CursorBefore: cursorBefore,
		Result:       result,
	})
	if err != nil {
		return LoopBAuditComposeResult{}, err
	}
	persisted, err := deps.Store.AppendRunRecord(runRecord)
	if err != nil {
		return LoopBAuditComposeResult{}, err
	}

	/* 8. Advisory artifacts (P11-T20) — rendered as DATA, never POSTed. */
	renderArtifacts := true
	if deps.RenderArtifacts != nil {
		renderArtifacts = *deps.RenderArtifacts
	}
	var artifacts *LoopBAuditArtifacts
	if renderArtifacts {
		artifacts = &LoopBAuditArtifacts{
			CheckRun: RenderDriftCheckRun(result.Findings, &persisted),
			Issue:    RenderDriftIssue(result.Findings, &persisted),
		}
	}

	return LoopBAuditComposeResult{
		Kind:      LoopBAuditRan,
		Trigger:   verdict.Trigger,
		Result:    &result,
		Findings:  result.Findings,
		RunRecord: &persisted,
		Artifacts: artifacts,
		Appended:  &appended,
	}, nil
}

/* ------------------------------------------------------- the cursor adapter */

// stateStoreCursorPort satisfies AuditCursorPort straight from the real
// StateStore (the ARCH §6.3 wiring: CAS discipline lives inside the store,
// R15).
type stateStoreCursorPort struct {
	store *state.StateStore
}

// ReadAuditCursor reads the audit cursor through the state store.
func (p stateStoreCursorPort) ReadAuditCursor() (state.StateRead[*schemas.AuditCursor], error) {
	return p.store.ReadAuditCursor()
}

// WriteAuditCursor writes the audit cursor CAS-guarded through the state
// store.
func (p stateStoreCursorPort) WriteAuditCursor(cursor schemas.AuditCursor, expectedHash *string) (state.CasWriteResult, error) {
	return p.store.WriteAuditCursor(cursor, expectedHash)
}
