package gitpr_test

// Ported from packages/core/test/gitpr-flow.test.ts — the "composed doc-PR
// flow (P6-T25/T26/T28)" describe block plus its FX6-1 composition-seam wave:
// the provenance gate (INV-10), the machine-PR content guard (INV-7), the
// layer-4 filter aborts (INV-2/3/4), the open-PR cap downgrade (P6-T06), the
// full happy path + idempotent re-run (P6-T28), the ledger CAS loser (R15),
// the closure-hoisted burst limiter (M1), the dirty-worktree pre-flight (M5),
// the real rebase + no-op drop + re-verify gate (M3), the open-scoped upsert
// with supersede-then-create and closed-prior re-creation (M2/M4), the
// cap-on-create-only rule (m10) and the runtime trace with no LLM seams
// (m4/V-P6-19).
//
// Same fixture family as flow_provenance_test.go (prefix `fpr`, same test
// package — real system git in fresh temp repos, the keyed in-memory platform
// double behind a call-counting wrapper, a recording incident sink, a constant
// ISO clock; no network, no wall clock anywhere (P19)). This file's own
// helpers carry the `fx` prefix.

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* ------------------------------------------------------------------ helpers */

// fxIntPtr is the int-pointer helper.
func fxIntPtr(value int) *int { return &value }

// fxStrPtr is the string-pointer helper.
func fxStrPtr(value string) *string { return &value }

// fxSourceKey is the fixture's merge-ledger source key (TS `pr-481`).
func fxSourceKey() string { return "pr-481" }

// fxSeedOpenPrs opens count docsync PRs on the in-memory platform (the TS
// pre-created open PRs for the cap tests).
func fxSeedOpenPrs(t *testing.T, f *fprFixture, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		_, err := f.inner.CreatePr(context.Background(), gitpr.CreatePrInput{
			Repo:   fprRepoSlug,
			Branch: "docsync/seed" + string(rune('a'+index)),
			Base:   "main",
			Title:  "docs: existing",
			Body:   "existing doc PR",
		})
		require.NoError(t, err)
	}
}

// fxRacyLedgerStore is the TS RacyLedgerStore: a concurrent winner lands a
// ledger entry on the FIRST merge-ledger write, so the flow's CAS loses (R15).
type fxRacyLedgerStore struct {
	*testkit.InMemoryStateStore
	winnerInserted bool
}

// WriteMergeLedger injects the winner entry once, then performs the flow's own
// write verbatim (the deterministic race simulation of the TS fixture).
func (s *fxRacyLedgerStore) WriteMergeLedger(
	sourceKey string,
	ledger schemas.MergeLedger,
	expectedHash *string,
) (state.CasWriteResult, error) {
	if !s.winnerInserted {
		s.winnerInserted = true
		read, err := s.InMemoryStateStore.ReadMergeLedger(sourceKey)
		if err != nil {
			return state.CasWriteResult{}, err
		}
		var baseHash *string
		if read.Exists {
			baseHash = &read.Hash
		}
		winner := schemas.MergeLedger{
			Schema:    gitpr.MergeLedgerSchemaVersion,
			SourceKey: sourceKey,
			UpdatedAt: fprClock(),
			Entries: []schemas.LedgerEntry{{
				DocPRNumber:    999,
				State:          schemas.MergeLedgerStateOpen,
				IdempotencyKey: "concurrent-winner-key",
				At:             fprClock(),
				Detail:         "a concurrent CI run won the ledger first",
			}},
		}
		if _, err := s.InMemoryStateStore.WriteMergeLedger(sourceKey, winner, baseHash); err != nil {
			return state.CasWriteResult{}, err
		}
	}
	return s.InMemoryStateStore.WriteMergeLedger(sourceKey, ledger, expectedHash)
}

// fxOnceGonePlatform is the platform wrapper whose FIRST CreatePr answers the
// API-410 (Gone) rejection — the upsert's one bounded retry must still ship
// exactly ONE PR (INV-11).
type fxOnceGonePlatform struct {
	gitpr.PrPlatformPort
	goneConsumed bool
}

func (p *fxOnceGonePlatform) CreatePr(
	ctx context.Context, input gitpr.CreatePrInput,
) (gitpr.PrRef, error) {
	if !p.goneConsumed {
		p.goneConsumed = true
		return gitpr.PrRef{}, &grStatusError{message: "Gone", status: 410}
	}
	return p.PrPlatformPort.CreatePr(ctx, input)
}

/* ------------------------------------------------------- the gates (INV-…) */

// TestINV10_ProvenanceLessFlowRefusedBeforeAnyPlatformCall ports INV-10: the
// provenance-gate rejection path — neither evidence form routes any PR write.
func TestINV10_ProvenanceLessFlowRefusedBeforeAnyPlatformCall(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.Provenance = nil
	input.ProvenanceRecord = nil
	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortProvenanceMissing)
	assert.Empty(t, result.Violations)

	// ZERO platform calls — the gate precedes every platform interaction.
	assert.Empty(t, f.platform.calls)
	// The incident was recorded (append-only evidence, never silent).
	require.Len(t, f.sink.recorded, 1)
	assert.Equal(t, schemas.SafetyIncidentRuleProvenanceMissing, f.sink.recorded[0].Rule)
	assert.Equal(t, fprRunID, f.sink.recorded[0].RunID)
	assert.Equal(t, fprDocPaths[0], f.sink.recorded[0].OffendingPath)
	// No branch was created either (the gate precedes ALL side effects).
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
}

// TestINV7_MachinePrTouchingManifestRejectedAndLogged ports INV-7: a machine PR
// touching the manifest is rejected and logged, before any side effect.
func TestINV7_MachinePrTouchingManifestRejectedAndLogged(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.Patches = []schemas.DocPatch{fprMakePatch("manifest.yml")}
	input.StateUpdates = nil
	input.DocContents = map[string]string{"manifest.yml": "version: 1\ndocuments: []\n"}
	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortGuardViolation)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "manifest_or_policy_edit", result.Violations[0].Rule)
	assert.Equal(t, "manifest.yml", result.Violations[0].Path)

	// Logged as a safety incident with the offending path + source PR.
	require.Len(t, f.sink.recorded, 1)
	assert.Equal(t, "manifest_or_policy_edit", string(f.sink.recorded[0].Rule))
	assert.Equal(t, "manifest.yml", f.sink.recorded[0].OffendingPath)
	require.NotNil(t, f.sink.recorded[0].SourcePRNumber)
	assert.Equal(t, fprSourcePr, *f.sink.recorded[0].SourcePRNumber)
	// No platform call, no branch — the guard precedes every side effect.
	assert.Empty(t, f.platform.calls)
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
}

// TestINV23_FilterViolationAbortsWithIncident ports INV-2/3/4: the layer-4
// re-diff of the ASSEMBLED branch catches out-of-allowlist and protected
// content, aborts BEFORE any platform call, and keeps the branch for
// forensics.
func TestINV23_FilterViolationAbortsWithIncident(t *testing.T) {
	// (a) INV-2/3 — an out-of-allowlist path in the ASSEMBLED branch diff: a
	// prior (foreign) commit on the doc branch added src/rogue.ts; the branch
	// tip descends from the run head, so the flow reuses it, commits its own
	// doc commit on top, and the layer-4 re-diff catches the rogue.
	f := fprMakeFixture(t)
	branch := "docsync/pr-481-" + f.headSha[:7]
	f.repo.git(t, "branch", branch, f.headSha)
	f.repo.git(t, "checkout", branch)
	f.repo.write(t, "src/rogue.ts", "export const rogue = true;\n")
	f.repo.commitAll(t, "foreign rogue commit")
	f.repo.git(t, "checkout", "main")

	resultA := f.runFlow(nil, nil)

	fprAssertAborted(t, resultA, gitpr.PrFlowAbortFilterViolation)
	assert.Contains(t, fxViolationRules(resultA), "out_of_allowlist_path")
	assert.Contains(t, fxViolationPaths(resultA), "src/rogue.ts")
	assert.Contains(t, fxIncidentRules(f), "out_of_allowlist_path")
	// Aborted BEFORE any platform call (filter runs before cap/burst/upsert).
	assert.Empty(t, f.platform.calls)
	// NO branch cleanup: the branch stays for forensics (documented posture).
	assert.True(t, f.branchExists(branch))

	// (b) INV-4 — a manifest-listed PROTECTED doc (locked) in the patch set:
	// the whole patch is rejected — no partial application survives to a PR.
	g := fprMakeFixture(t)
	manifest := fprMakeManifest()
	locked := schemas.DocModeLocked
	manifest.Documents[0].Mode = &locked
	inputB := g.makeInput()
	inputB.Manifest = manifest
	resultB := g.runFlow(nil, &inputB)

	fprAssertAborted(t, resultB, gitpr.PrFlowAbortFilterViolation)
	require.Len(t, resultB.Violations, 1)
	assert.Equal(t, "protected_path_patch", resultB.Violations[0].Rule)
	assert.Equal(t, "docs/payments.md", resultB.Violations[0].Path)
	assert.Contains(t, fxIncidentRules(g), "protected_path_patch")
	// Whole-patch rejection: NO PR exists at all (the clean docs did not ship).
	assert.Empty(t, g.platform.calls)
	assert.Empty(t, g.inner.Prs(fprRepoSlug))
}

// fxViolationRules / fxViolationPaths / fxIncidentRules render the wire rule and
// path columns of an aborted result / the recorded incidents.
func fxViolationRules(result gitpr.PrFlowResult) []string {
	rules := make([]string, 0, len(result.Violations))
	for _, violation := range result.Violations {
		rules = append(rules, violation.Rule)
	}
	return rules
}

func fxViolationPaths(result gitpr.PrFlowResult) []string {
	paths := make([]string, 0, len(result.Violations))
	for _, violation := range result.Violations {
		paths = append(paths, violation.Path)
	}
	return paths
}

func fxIncidentRules(f *fprFixture) []string {
	rules := make([]string, 0, len(f.sink.recorded))
	for _, incident := range f.sink.recorded {
		rules = append(rules, string(incident.Rule))
	}
	return rules
}

/* ------------------------------------------------------- the rate governors */

// TestP6T06_CapDowngradeNeverCallsPlatformCreate ports P6-T06: at/over the
// open-PR cap a CREATE downgrades to report-only — the ONLY platform call is
// the cap READ (the marker lookup), never a create/update.
func TestP6T06_CapDowngradeNeverCallsPlatformCreate(t *testing.T) {
	f := fprMakeFixture(t)
	// Three open docsync PRs already exist → the default cap (3) is reached.
	fxSeedOpenPrs(t, f, 3)
	openPrs, err := f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	require.Len(t, openPrs, 3)

	result := f.runFlow(nil, nil)

	require.Equal(t, gitpr.PrFlowKindDowngradedReportOnly, result.Kind)
	assert.Equal(t, string(gitpr.PrFlowDowngradeOpenPrCapReached), result.Reason)
	assert.Equal(t, gitpr.PatchReadyMessage, result.Message)
	assert.Equal(t, "patch ready, apply manually", result.Message)
	assert.Equal(t, 3, result.OpenCount)
	assert.Equal(t, 3, result.MaxOpen)
	// MECHANICAL ADAPTATION (m10/m4): the composed flow's open-marker lookup (a
	// READ) precedes the cap evaluation — the invariant under test is that the
	// ONLY write-class call is absent and the cap READ happened.
	assert.Equal(t, []string{"listOpenDocsPrs"}, fxCallsExcept(f, "findByPrBodyMarker"))
	assert.NotContains(t, f.platform.calls, "createPr")
	assert.NotContains(t, f.platform.calls, "updatePr")
	// A downgrade is report-only, not a safety abort — no incident.
	assert.Empty(t, f.sink.recorded)
}

// fxCallsExcept renders the platform calls minus the given reads.
func fxCallsExcept(f *fprFixture, skip string) []string {
	calls := make([]string, 0, len(f.platform.calls))
	for _, call := range f.platform.calls {
		if call == skip {
			continue
		}
		calls = append(calls, call)
	}
	return calls
}

// TestM10_CapDoesNotCountTheUpdatedPr ports m10: the update path of a same-key
// prior SKIPS the cap entirely (no READ, no downgrade, no new PR).
func TestM10_CapDoesNotCountTheUpdatedPr(t *testing.T) {
	f := fprMakeFixture(t)
	first := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, first.Kind)

	// Fill the cap: 1 (the flow's own PR) + 2 = 3 open docsync PRs → AT the
	// default cap of 3. A CREATE would now downgrade — an UPDATE must not.
	fxSeedOpenPrs(t, f, 2)
	openPrs, err := f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	require.Len(t, openPrs, 3)
	callsBefore := len(f.platform.calls)

	second := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindUpdated, second.Kind)

	// The update path SKIPPED the cap entirely: no listOpenDocsPrs READ, no
	// downgrade, no new PR — the same-key prior is updated in place.
	runTwoCalls := f.platform.calls[callsBefore:]
	assert.NotContains(t, runTwoCalls, "listOpenDocsPrs")
	assert.NotContains(t, runTwoCalls, "createPr")
	assert.Contains(t, runTwoCalls, "updatePr")
	assert.Len(t, f.inner.Prs(fprRepoSlug), 3)
	openPrs, err = f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	assert.Contains(t, fxPrNumbers(openPrs), first.PR.Number)
}

// fxPrNumbers renders the PR numbers of a ref list.
func fxPrNumbers(prs []gitpr.PrRef) []int {
	numbers := make([]int, 0, len(prs))
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	return numbers
}

// TestM1_BurstGovernorQueuesThroughTheComposedFlow ports M1: the burst limiter
// is CLOSURE-HOISTED — the second run AWAITS through the injected delay
// (queue-not-fail, INV-11) and still ships on the same single PR.
func TestM1_BurstGovernorQueuesThroughTheComposedFlow(t *testing.T) {
	f := fprMakeFixture(t)
	// Fake clock + a delay primitive that ADVANCES the fake clock: the burst
	// limiter's queue-not-fail wait is observable with zero real sleeping.
	parsed, err := time.Parse(time.RFC3339Nano, fprClock())
	require.NoError(t, err)
	nowMs := parsed.UnixMilli()
	var delays []int64
	clock := func() string {
		return time.UnixMilli(nowMs).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	delay := func(_ context.Context, ms int64) error {
		delays = append(delays, ms)
		nowMs += ms
		return nil
	}
	budgets := &gitpr.DocPrFlowBudgets{MaxPerHour: fxIntPtr(1)}
	input := f.makeInput()
	input.Budgets = budgets

	// ONE flow instance — the burst limiter is hoisted into it, so its
	// rolling-window state survives across the two Run calls (M1/INV-11).
	flow := f.makeFlow(func(deps *gitpr.DocPrFlowDeps) {
		deps.Clock = clock
		deps.Delay = delay
	})
	first, err := flow.Run(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, gitpr.PrFlowKindCreated, first.Kind)
	assert.Empty(t, delays) // within budget → immediate admit

	// The SECOND run goes through the SAME closure-hoisted limiter (M1): the
	// rolling window still holds run 1's admission → the admit WAITS until it
	// ages out, then ships.
	second, err := flow.Run(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, gitpr.PrFlowKindUpdated, second.Kind)
	assert.Equal(t, []int64{gitpr.BurstWindowMs}, delays)
	// Queued, not failed, not duplicated: still exactly ONE open PR.
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
	openPrs, err := f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	assert.Equal(t, []int{first.PR.Number}, fxPrNumbers(openPrs))
}

// TestRun_BurstBudgetChangeBetweenRunsIsAConfigRefusal ports the limiterFor
// guard: one flow instance carries ONE burst budget (INV-11) — a changed budget
// is a typed config refusal, never a silent rolling-window reset.
func TestRun_BurstBudgetChangeBetweenRunsIsAConfigRefusal(t *testing.T) {
	f := fprMakeFixture(t)
	flow := f.makeFlow()

	first := f.makeInput()
	first.Budgets = &gitpr.DocPrFlowBudgets{MaxPerHour: fxIntPtr(5)}
	firstResult, err := flow.Run(context.Background(), first)
	require.NoError(t, err)
	require.Equal(t, gitpr.PrFlowKindCreated, firstResult.Kind)

	second := f.makeInput()
	second.Budgets = &gitpr.DocPrFlowBudgets{MaxPerHour: fxIntPtr(7)}
	_, err = flow.Run(context.Background(), second)
	require.Error(t, err)
	assert.Equal(t, gitpr.EDOCSYNCDocPrFlowConfig, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "burst budget changed between runs")
}

// TestRun_NonIsoClockLatchesAConfigError pins the burst clock's fail-closed
// projection: a non-ISO clock cannot derive nowMs, so the flow refuses after
// the admission instead of guessing a timestamp (P19).
func TestRun_NonIsoClockLatchesAConfigError(t *testing.T) {
	f := fprMakeFixture(t)
	flow := f.makeFlow(func(deps *gitpr.DocPrFlowDeps) {
		deps.Clock = func() string { return "not-an-iso-timestamp" }
	})
	_, err := flow.Run(context.Background(), f.makeInput())
	require.Error(t, err)
	assert.Equal(t, gitpr.EDOCSYNCDocPrFlowConfig, docutil.CodeOf(err))
	assert.Contains(t, err.Error(), "ISO-8601")
}

/* -------------------------------------------------------- the upsert (M2/M4) */

// TestP6T28_FullHappyPathCreatesOneAggregatedPr ports P6-T28: the whole
// verified doc set ships as ONE aggregated PR on ONE doc branch, recorded OPEN
// in the merge-order ledger.
func TestP6T28_FullHappyPathCreatesOneAggregatedPr(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow(nil, nil)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	assert.False(t, result.CasConflict)
	assert.False(t, result.RerunRequired)
	assert.True(t, result.BranchCreated)
	assert.Equal(t, "docsync/pr-481-"+f.headSha[:7], result.Branch)
	require.NotNil(t, result.CommitSha)

	// ONE aggregated PR for the whole verified doc set.
	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 1)
	require.NotNil(t, result.PR)
	assert.Equal(t, result.PR.Number, prs[0].Number)
	// The body carries the banner, the provenance summary, the partial-pass
	// report and the hidden idempotency marker.
	assert.True(t, strings.HasPrefix(prs[0].Body, gitpr.PRBodyBanner+"\n"))
	assert.Contains(t, prs[0].Body, "## Provenance")
	assert.Contains(t, prs[0].Body, "- Run: `"+fprRunID+"`")
	assert.Contains(t, prs[0].Body, "- Source PR: #481")
	assert.Contains(t, prs[0].Body, "## Partial pass (D4)")
	for _, docPath := range fprDocPaths {
		assert.Contains(t, prs[0].Body, docPath)
	}
	assert.Contains(t, prs[0].Body, "<!-- docsync:idempotency:"+result.IdempotencyKey+" -->")
	require.NotNil(t, result.PartialPass)
	assert.Len(t, result.PartialPass.Included, 3)
	assert.Len(t, result.PartialPass.Dropped, 0)

	// Merge-order ledger: the doc PR is recorded OPEN for the source key.
	ledger := f.loadLedger(fxSourceKey())
	require.NotNil(t, ledger)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, result.PR.Number, ledger.Entries[0].DocPRNumber)
	assert.Equal(t, schemas.MergeLedgerStateOpen, ledger.Entries[0].State)
	assert.Equal(t, result.IdempotencyKey, ledger.Entries[0].IdempotencyKey)
	require.NotNil(t, result.LedgerHash)

	// Diff scope — exactly the three docs + the state carriage, nothing else.
	assert.Equal(t, []string{
		".docsync/state/runs/run_flow_01J.json",
		"docs/api.md",
		"docs/cli.md",
		"docs/payments.md",
	}, f.diffAgainstMain(result.Branch))
	// The commit lands ON the doc branch (never the default branch, INV-1).
	assert.Equal(t, *result.CommitSha, strings.TrimSpace(f.repo.git(t, "rev-parse", result.Branch)))
	assert.NotEqual(t, f.headSha, *result.CommitSha)
}

// TestP6T28_IdempotentRerunUpdatesNotDuplicates ports P6-T28: the same key
// re-run UPDATES the same PR (never duplicates), the branch is REUSED, and the
// ledger skip is the idempotent act.
func TestP6T28_IdempotentRerunUpdatesNotDuplicates(t *testing.T) {
	f := fprMakeFixture(t)
	first := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, first.Kind)

	// Same key (same source head sha + same doc set) → the SAME PR, updated.
	second := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindUpdated, second.Kind)
	require.NotNil(t, first.PR)
	require.NotNil(t, second.PR)
	assert.Equal(t, first.PR.Number, second.PR.Number)
	assert.Equal(t, first.IdempotencyKey, second.IdempotencyKey)
	// The re-run's write was a content no-op → no new commit (typed tolerance).
	assert.Nil(t, second.CommitSha)
	// Exactly ONE PR and exactly ONE doc branch (the prior run's branch is
	// REUSED — its tip descends from the run head; no -2 junk branch).
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
	assert.True(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]+"-2"))
	// The ledger skip is the idempotent act (the entry is already open) — no
	// CAS conflict, no incident.
	assert.False(t, second.CasConflict)
	assert.False(t, second.RerunRequired)
	assert.Empty(t, f.sink.recorded)
}

// TestFlow_UpsertSurvivesASingleApi410ThenShips pins the creation-only 410
// retry through the composed flow: one bounded retry of the SAME creation
// still ships exactly ONE PR (INV-11).
func TestFlow_UpsertSurvivesASingleApi410ThenShips(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
		deps.Platform = &fxOnceGonePlatform{PrPlatformPort: deps.Platform}
	}}, nil)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	// The marker-keyed retry is idempotent: ONE PR, never a duplicate.
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
	assert.Contains(t, f.platform.calls, "createPr")
}

// TestM2_StaleOpenPrSupersededNotDuplicated ports M2: a NEW idempotency key
// with an open prior SUPERSEDES the stale PR (marker comment + closed) and
// creates the replacement — never a duplicate.
func TestM2_StaleOpenPrSupersededNotDuplicated(t *testing.T) {
	f := fprMakeFixture(t)
	first := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, first.Kind)
	require.NotNil(t, first.PR)

	// The source PR evolves: a new head commit → a NEW idempotency key.
	f.repo.git(t, "checkout", "main")
	f.repo.write(t, "README.md", "# widget — evolved\n")
	head2 := f.repo.commitAll(t, "source PR evolves")

	second := f.runFlow(nil, f.withSourceHeadSha(f.makeInput(), head2))
	require.Equal(t, gitpr.PrFlowKindCreated, second.Kind)
	require.NotNil(t, second.PR)

	// The stale open PR was SUPERSEDED (marker comment + closed), not duplicated.
	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 2)
	stale, staleOK := fxPrByNumber(prs, first.PR.Number)
	require.True(t, staleOK)
	assert.Equal(t, gitpr.PrStateClosed, stale.State)
	assert.Contains(t, f.inner.CommentsFor(first.PR.Number),
		"superseded by newer analysis (#"+strconv.Itoa(second.PR.Number)+")")
	// Exactly ONE open doc PR remains — the replacement.
	openPrs, err := f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	require.Len(t, openPrs, 1)
	assert.Equal(t, second.PR.Number, openPrs[0].Number)

	// The ledger recorded the transition: open → superseded, plus the new open.
	ledger := f.loadLedger(fxSourceKey())
	require.NotNil(t, ledger)
	staleEntry, staleEntryOK := fxLedgerEntry(ledger, first.PR.Number)
	require.True(t, staleEntryOK)
	assert.Equal(t, schemas.MergeLedgerStateSuperseded, staleEntry.State)
	openEntry, openEntryOK := fxLedgerEntry(ledger, second.PR.Number)
	require.True(t, openEntryOK)
	assert.Equal(t, schemas.MergeLedgerStateOpen, openEntry.State)
	assert.Equal(t, second.IdempotencyKey, openEntry.IdempotencyKey)
}

// withSourceHeadSha swaps the source PR's head sha (TS input override).
func (f *fprFixture) withSourceHeadSha(input gitpr.DocPrFlowInput, headSha string) *gitpr.DocPrFlowInput {
	input.SourcePr = &gitpr.FlowSourcePr{Number: fprSourcePr, HeadSha: headSha}
	return &input
}

// fxPrByNumber finds a PR ref by number.
func fxPrByNumber(prs []gitpr.PrRef, number int) (gitpr.PrRef, bool) {
	for _, pr := range prs {
		if pr.Number == number {
			return pr, true
		}
	}
	return gitpr.PrRef{}, false
}

// fxLedgerEntry finds a ledger entry by doc-PR number.
func fxLedgerEntry(ledger *schemas.MergeLedger, docPrNumber int) (schemas.LedgerEntry, bool) {
	for _, entry := range ledger.Entries {
		if entry.DocPRNumber == docPrNumber {
			return entry, true
		}
	}
	return schemas.LedgerEntry{}, false
}

// TestM4_ClosedPriorReCreatesWithDisambiguator ports M4: a CLOSED prior
// carrying the same marker re-CREATES on the factory's -2 disambiguated branch
// — the closed PR is never mutated.
func TestM4_ClosedPriorReCreatesWithDisambiguatedBranch(t *testing.T) {
	f := fprMakeFixture(t)
	first := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, first.Kind)
	require.NotNil(t, first.PR)

	// The prior doc PR was MERGED (closed on the platform; the ledger still
	// carries its open entry until post-merge handling lands) — a re-run of the
	// SAME doc set is a legitimate RE-CREATION.
	require.NoError(t, f.inner.ClosePr(context.Background(), *first.PR, "merged via squash"))
	callsBefore := len(f.platform.calls)

	second := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, second.Kind)
	require.NotNil(t, second.PR)

	// NEW PR number, DISAMBIGUATED branch (the factory's -2 rule), no throw.
	assert.NotEqual(t, first.PR.Number, second.PR.Number)
	assert.Equal(t, "docsync/pr-481-"+f.headSha[:7]+"-2", second.Branch)
	assert.True(t, f.branchExists(second.Branch))
	assert.Len(t, f.inner.Prs(fprRepoSlug), 2)
	// The CLOSED prior was never mutated: no update, no supersede, no close.
	runTwoCalls := f.platform.calls[callsBefore:]
	assert.NotContains(t, runTwoCalls, "updatePr")
	assert.NotContains(t, runTwoCalls, "markSuperseded")
	assert.Contains(t, runTwoCalls, "createPr")
	// Fresh ledger 'open' entry for the new PR — no transition error.
	ledger := f.loadLedger(fxSourceKey())
	require.NotNil(t, ledger)
	newEntry, newEntryOK := fxLedgerEntry(ledger, second.PR.Number)
	require.True(t, newEntryOK)
	assert.Equal(t, schemas.MergeLedgerStateOpen, newEntry.State)
	assert.Equal(t, second.IdempotencyKey, newEntry.IdempotencyKey)
}

// TestR15_LedgerCasLoserMarksRerunRequired ports R15: a concurrent winner
// lands between the flow's fresh read and its CAS write — the PR SHIPPED, the
// result is annotated, the WINNER's bytes stand.
func TestR15_LedgerCasLoserMarksRerunRequired(t *testing.T) {
	f := fprMakeFixture(t)
	racy := &fxRacyLedgerStore{InMemoryStateStore: f.store}
	result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
		deps.Store = racy
	}}, nil)

	// The PR itself SHIPPED (created) — the ledger loss annotates, never undoes.
	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	assert.True(t, result.CasConflict)
	assert.True(t, result.RerunRequired)
	require.NotNil(t, result.LedgerDetail)
	assert.Contains(t, *result.LedgerDetail, "CAS conflict")
	assert.Contains(t, *result.LedgerDetail, "R15")

	// The WINNER's bytes stand — the loser never overwrote the ledger.
	ledger := f.loadLedger(fxSourceKey())
	require.NotNil(t, ledger)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, 999, ledger.Entries[0].DocPRNumber)
}

/* ------------------------------------------------- the M3 merge-order gate */

// TestM3_DefaultBranchAdvancedReverifyPassShips ports M3: the default branch
// ADVANCED past the analysis anchor → the merged content RE-PASSES verification
// through the injected hook and ships on the really-rebased branch.
func TestM3_DefaultBranchAdvancedReverifyPassShips(t *testing.T) {
	f := fprMakeFixture(t)
	// The default branch ADVANCES after the analysis was anchored at f.headSha.
	f.repo.write(t, "README.md", "# widget — upstream moved\n")
	newMainHead := f.repo.commitAll(t, "upstream advance")

	type reverifyCall struct {
		docPath     string
		headContent string
	}
	var reverifyCalls []reverifyCall
	reverify := func(patch *schemas.DocPatch, headContent string) *schemas.VerificationReport {
		reverifyCalls = append(reverifyCalls, reverifyCall{docPath: patch.DocPath, headContent: headContent})
		return &schemas.VerificationReport{
			Schema:           "docsync.verification-report/v1",
			RunID:            fprRunID,
			DocPath:          patch.DocPath,
			PatchRef:         "patch-ref",
			Verdict:          schemas.VerdictPass,
			AutoEligible:     true,
			AutoBlockReasons: []string{},
			PolicyVersion:    17,
		}
	}

	input := f.makeInput()
	input.SourcePr = &gitpr.FlowSourcePr{Number: fprSourcePr, HeadSha: f.headSha}
	input.RunHeadSha = fxStrPtr(f.headSha)
	result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
		deps.Reverify = reverify
	}}, &input)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	// Every surviving patch was re-verified against the MERGED branch content.
	reverifiedPaths := make([]string, 0, len(reverifyCalls))
	for _, call := range reverifyCalls {
		reverifiedPaths = append(reverifiedPaths, call.docPath)
		assert.NotEmpty(t, call.headContent)
	}
	sort.Strings(reverifiedPaths)
	expected := append([]string(nil), fprDocPaths...)
	sort.Strings(expected)
	assert.Equal(t, expected, reverifiedPaths)
	// The REAL rebase happened: the doc commit was replayed onto the new
	// default-branch head (its parent IS the advanced main head).
	assert.Equal(t, newMainHead, strings.TrimSpace(
		f.repo.git(t, "log", "--format=%P", "-n", "1", result.Branch)))
	require.NotNil(t, result.CommitSha)
	assert.Equal(t, strings.TrimSpace(f.repo.git(t, "rev-parse", result.Branch)), *result.CommitSha)
	assert.Empty(t, result.DroppedNoOps)
	assert.Contains(t, f.platform.calls, "createPr")
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
}

// TestM3_ReverifyUnavailableFailsClosed ports M3: a moved base with an ABSENT
// hook fails CLOSED — abstain, never ship unverified merged content (A9).
func TestM3_ReverifyUnavailableFailsClosed(t *testing.T) {
	f := fprMakeFixture(t)
	// Base moved + NO re-verify hook → fail CLOSED.
	f.repo.write(t, "README.md", "# widget — upstream moved again\n")
	f.repo.commitAll(t, "upstream advance")

	input := f.makeInput()
	input.SourcePr = &gitpr.FlowSourcePr{Number: fprSourcePr, HeadSha: f.headSha}
	input.RunHeadSha = fxStrPtr(f.headSha)
	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortReverifyUnavailable)
	assert.Contains(t, result.Detail, "re-verify")
	// No platform write of any kind: nothing unverified ever ships.
	assert.NotContains(t, f.platform.calls, "createPr")
	assert.NotContains(t, f.platform.calls, "updatePr")
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
}

// TestM5_DirtyWorktreeAbortsBeforeRefCreation ports M5: a dirty worktree
// aborts `dirty-worktree` before ANY ref creation and any platform call.
func TestM5_DirtyWorktreeAbortsBeforeRefCreation(t *testing.T) {
	f := fprMakeFixture(t)
	// A TRACKED, uncommitted modification is real dirt (the flow's own ledger
	// bookkeeping is git-ignored by the fixture — by design).
	f.repo.write(t, "docs/api.md", "# api — uncommitted edit\n")
	branchesBefore := f.localBranches()

	result := f.runFlow(nil, nil)

	fprAssertAborted(t, result, gitpr.PrFlowAbortDirtyWorktree)
	assert.Contains(t, result.Detail, "not clean")
	// The pre-flight fired before ANY ref creation: the branch set is
	// byte-identical before/after the run.
	assert.Equal(t, branchesBefore, f.localBranches())
	// And before any platform interaction.
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.sink.recorded)
}

/* ------------------------------------------------ no-op drop + partial pass */

// fxNoopBaseContents are the fixture docs' base contents (the run head the
// analysis was anchored to) — the noop patches are anchored line-exact to
// them, exactly like the TS gateTestPatch over gateBaseDocVar.
var fxNoopBaseContents = map[string]string{
	"docs/payments.md": "# payments\n\nRefunds require an idempotency key.\n",
	"docs/api.md":      "# api\n\nEndpoint list lives here.\n",
	"docs/cli.md":      "# cli\n\nCommands live here.\n",
}

// fxAlreadyFixed renders the "upstream already applied this fix" content of a
// base doc (the P6-T15 strict no-op scenario: the head content already carries
// the patch's intended result).
func fxAlreadyFixed(baseContent string) string {
	lines := strings.Split(strings.TrimSuffix(baseContent, "\n"), "\n")
	lines[len(lines)-1] = lines[len(lines)-1] + " for amounts above 10 USD."
	return strings.Join(lines, "\n") + "\n"
}

// fxNoopDropPatch builds a patch whose single hunk replaces the base doc's
// THIRD line; its base_content_hash hashes the base doc, so the flow's
// merge-base snapshot is accepted as the honest 3-way basis.
func fxNoopDropPatch(docPath, baseContent string) schemas.DocPatch {
	lines := strings.Split(strings.TrimSuffix(baseContent, "\n"), "\n")
	stale := lines[2]
	patch := schemas.DocPatchExample
	patch.RunID = fprRunID
	patch.DocPath = docPath
	patch.BaseContentHash = docutil.ContentHash(baseContent)
	patch.Hunks = []schemas.PatchHunk{{
		Anchor: schemas.PatchAnchor{
			StartLine:   3,
			EndLine:     3,
			ContentHash: claims.HashSpan(stale),
		},
		DeleteText:   stale,
		InsertText:   stale + " for amounts above 10 USD.",
		EvidenceRefs: schemas.DocPatchExample.Hunks[0].EvidenceRefs,
		ClaimIDs:     schemas.DocPatchExample.Hunks[0].ClaimIDs,
	}}
	return patch
}

// fxNoopFlowInput builds the flow input for the no-op-drop scenarios: every
// patch is a properly-anchored noop candidate, and alreadyUpstream selects
// which docs the default branch already carries fixed.
func fxNoopFlowInput(f *fprFixture, alreadyUpstream map[string]bool) gitpr.DocPrFlowInput {
	input := f.makeInput()
	patches := make([]schemas.DocPatch, 0, len(fprDocPaths))
	docContents := make(map[string]string, len(fprDocPaths))
	fixedAll := make(map[string]string, len(fprDocPaths))
	for _, docPath := range fprDocPaths {
		baseContent := fxNoopBaseContents[docPath]
		fixed := fxAlreadyFixed(baseContent)
		fixedAll[docPath] = fixed
		patches = append(patches, fxNoopDropPatch(docPath, baseContent))
		if alreadyUpstream[docPath] {
			docContents[docPath] = fixed
		} else {
			docContents[docPath] = input.DocContents[docPath]
		}
	}
	input.Patches = patches
	input.DocContents = docContents
	return input
}

// fxAdvanceMainUpstream carries the given fixed contents onto the default
// branch (the world WITHOUT this flow's commit) and returns the new head.
func fxAdvanceMainUpstream(t *testing.T, f *fprFixture, fixedContents map[string]string) string {
	t.Helper()
	f.repo.git(t, "checkout", "main")
	for _, docPath := range fprDocPaths {
		if fixed, ok := fixedContents[docPath]; ok {
			f.repo.write(t, docPath, fixed)
		}
	}
	return f.repo.commitAll(t, "upstream already applied the doc fixes")
}

// TestM3b_AllPatchesDropAsNoOpsIsANoOpRun ports M3b: when EVERY surviving
// patch is a byte-identical no-op against the default-branch head, ALL patches
// drop — no PR noise (PLAN §12.3), no platform call, the branch stays.
func TestM3b_AllPatchesDropAsNoOpsIsANoOpRun(t *testing.T) {
	f := fprMakeFixture(t)
	// The default branch already carries EVERY intended change (upstream
	// applied them first) → all three patches are byte-identical no-ops.
	allUpstream := map[string]bool{
		"docs/payments.md": true,
		"docs/api.md":      true,
		"docs/cli.md":      true,
	}
	fixed := map[string]string{}
	for _, docPath := range fprDocPaths {
		fixed[docPath] = fxAlreadyFixed(fxNoopBaseContents[docPath])
	}
	fxAdvanceMainUpstream(t, f, fixed)

	input := fxNoopFlowInput(f, allUpstream)
	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindNoOp, result.Kind)
	require.Len(t, result.DroppedNoOps, len(fprDocPaths))
	dropped := make([]string, 0, len(result.DroppedNoOps))
	for _, drop := range result.DroppedNoOps {
		dropped = append(dropped, drop.DocPath)
		assert.Contains(t, drop.Detail, "no-op patch dropped")
	}
	assert.Equal(t, fprDocPaths, dropped)
	// No PR noise: no platform call of any kind.
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
	// The branch stays for forensics (no cleanup, ever).
	assert.True(t, f.branchExists(result.Branch))
	require.NotNil(t, result.PartialPass)
	assert.Empty(t, result.PartialPass.Included)
}

// TestM3b_PartialNoOpDropShipsTheGenuineDrift ports the M3b partial case: a
// byte-identical no-op patch is DROPPED from the PR set and reported, while the
// genuine drift still ships.
func TestM3b_PartialNoOpDropShipsTheGenuineDrift(t *testing.T) {
	f := fprMakeFixture(t)
	// Only docs/api.md was already fixed upstream.
	alreadyUpstream := map[string]bool{"docs/api.md": true}
	fixed := map[string]string{"docs/api.md": fxAlreadyFixed(fxNoopBaseContents["docs/api.md"])}
	fxAdvanceMainUpstream(t, f, fixed)

	input := fxNoopFlowInput(f, alreadyUpstream)
	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	require.Len(t, result.DroppedNoOps, 1)
	assert.Equal(t, "docs/api.md", result.DroppedNoOps[0].DocPath)
	assert.NotEmpty(t, result.DroppedNoOps[0].Detail)
	// The dropped doc's content does NOT ride the PR; the genuine drift does.
	changed := f.diffAgainstMain(result.Branch)
	assert.NotContains(t, changed, "docs/api.md")
	assert.Contains(t, changed, "docs/payments.md")
	assert.Contains(t, changed, "docs/cli.md")
	assert.Contains(t, changed, fprStatePath)
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
}

// TestV_P6_1_ComposedMixedOutcomeFailingDocDropped ports V-P6-1: a failing
// verification row is DROPPED in-flow (D4: reported, never silently) while the
// passing majority still ships.
func TestV_P6_1_ComposedMixedOutcomeFailingDocDropped(t *testing.T) {
	f := fprMakeFixture(t)
	outcome := fprMakeOutcome()
	// docs[2] (docs/cli.md) FAILS verification → dropped IN-FLOW (D4).
	outcome.Documents[2].Verification = &schemas.RunDocumentVerification{
		Verdict:          schemas.VerdictReject,
		AutoEligible:     false,
		AutoBlockReasons: []string{schemas.AutoBlockUnanchoredClaims},
	}
	input := f.makeInput()
	input.Outcome = outcome
	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)

	// The drop is REPORTED in the PR body (partial-pass section).
	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 1)
	assert.Contains(t, prs[0].Body, "## Partial pass (D4)")
	assert.Contains(t, prs[0].Body, "docs/cli.md")
	// The failing doc's content does NOT ship: the branch diff excludes it.
	changed := f.diffAgainstMain(result.Branch)
	assert.NotContains(t, changed, "docs/cli.md")
	assert.Contains(t, changed, "docs/payments.md")
	assert.Contains(t, changed, "docs/api.md")
	// The partial-pass view: 2 included, exactly the failing doc dropped.
	require.NotNil(t, result.PartialPass)
	included := make([]string, 0, len(result.PartialPass.Included))
	for _, doc := range result.PartialPass.Included {
		included = append(included, doc.DocPath)
	}
	sort.Strings(included)
	assert.Equal(t, []string{"docs/api.md", "docs/payments.md"}, included)
	require.Len(t, result.PartialPass.Dropped, 1)
	assert.Equal(t, "docs/cli.md", result.PartialPass.Dropped[0].DocPath)
	assert.NotEmpty(t, result.PartialPass.Dropped[0].Reason)
}

// TestFlow_NothingVerificationPassingIsANoOpRun ports the classification floor:
// when nothing verification-passing survives there is no PR, no platform call,
// no commit — and the drop is reported through the partial-pass view.
func TestFlow_NothingVerificationPassingIsANoOpRun(t *testing.T) {
	f := fprMakeFixture(t)
	outcome := fprMakeOutcome()
	for index := range outcome.Documents {
		outcome.Documents[index].Verification = &schemas.RunDocumentVerification{
			Verdict:          schemas.VerdictReject,
			AutoEligible:     false,
			AutoBlockReasons: []string{schemas.AutoBlockUnanchoredClaims},
		}
	}
	input := f.makeInput()
	input.Outcome = outcome
	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindNoOp, result.Kind)
	assert.Empty(t, result.DroppedNoOps)
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
	require.NotNil(t, result.PartialPass)
	assert.Empty(t, result.PartialPass.Included)
	assert.Len(t, result.PartialPass.Dropped, 3)
}

// TestP6T19_SelfTriggeredRunAborts ports P6-T19: a run whose source PR is
// docsync's OWN doc PR aborts `self-triggered` — no incident, no platform call
// (loop prevention is not a safety violation).
func TestP6T19_SelfTriggeredRunAborts(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.SourcePr = &gitpr.FlowSourcePr{
		Number:     fprSourcePr,
		HeadSha:    f.headSha,
		HeadBranch: fxStrPtr("docsync/pr-481-abcdef"),
		Author:     fxStrPtr("docsync-bot"),
	}
	result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
		deps.OwnIdentity = "docsync-bot"
	}}, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortSelfTriggered)
	assert.Contains(t, result.Detail, "docsync/pr-481-abcdef")
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.sink.recorded)
}

/* ----------------------------------------------------------- the P19 trace */

// TestV_P6_19_RuntimeTraceShowsNoLlmSeams ports V-P6-19: the runtime trace is
// non-empty and carries ONLY deterministic seams — not one entry names an LLM
// seam (decision/writer/judge).
func TestV_P6_19_RuntimeTraceShowsNoLlmSeams(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)

	require.NotEmpty(t, result.Trace)
	seams := map[gitpr.PrFlowTraceSeam]bool{}
	for _, entry := range result.Trace {
		assert.NotEmpty(t, entry.Op)
		assert.False(t, gitpr.LLMSeamNames[string(entry.Seam)])
		seams[entry.Seam] = true
	}
	assert.True(t, seams[gitpr.PrFlowTraceGit])
	assert.True(t, seams[gitpr.PrFlowTracePlatform])
	assert.True(t, seams[gitpr.PrFlowTraceLedger])
	assert.False(t, seams[gitpr.PrFlowTraceState])
}

// TestV_P6_2_ThreeConsecutiveRunsZeroDuplicatePrs ports V-P6-2: three
// consecutive runs produce created/updated/updated with exactly ONE aggregated
// PR and no disambiguator junk branches.
func TestV_P6_2_ThreeConsecutiveRunsZeroDuplicatePrs(t *testing.T) {
	f := fprMakeFixture(t)
	kinds := make([]gitpr.PrFlowResultKind, 0, 3)
	for index := 0; index < 3; index++ {
		result := f.runFlow(nil, nil)
		kinds = append(kinds, result.Kind)
	}
	assert.Equal(t, []gitpr.PrFlowResultKind{
		gitpr.PrFlowKindCreated,
		gitpr.PrFlowKindUpdated,
		gitpr.PrFlowKindUpdated,
	}, kinds)
	// Zero duplicates across three consecutive runs: ONE aggregated PR.
	assert.Len(t, f.inner.Prs(fprRepoSlug), 1)
	openPrs, err := f.inner.ListOpenDocsPrs(context.Background(), fprRepoSlug)
	require.NoError(t, err)
	assert.Len(t, openPrs, 1)
	// And no disambiguator junk branches.
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]+"-2"))
}

/* --------------------------------------------------------- the local-run form */

// TestFlow_LocalRunWithoutSourcePrShips pins the source-PR-less projection:
// the run-scoped branch name, the local run title, the run-id idempotency key
// and the local ledger source key.
func TestFlow_LocalRunWithoutSourcePrShips(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.SourcePr = nil
	input.RunHeadSha = nil
	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	assert.Equal(t, "docsync/run_flow_01J", result.Branch)
	require.NotNil(t, result.PR)
	assert.Equal(t, "docs: synchronize documentation (local run "+fprRunID+")", result.PR.Title)

	ledger := f.loadLedger("local-" + fprRunID)
	require.NotNil(t, ledger)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, result.PR.Number, ledger.Entries[0].DocPRNumber)
	assert.Equal(t, result.IdempotencyKey, ledger.Entries[0].IdempotencyKey)
}

/* ------------------------------------------------------- the post-merge gate */

// TestFlow_PostMergeGateSeesTheShippedDocPr composes the shipped flow result
// with the post-merge re-run gate (P6-T17): a default-branch push with the
// flow's open doc PR based on it gates a re-run naming that PR.
func TestFlow_PostMergeGateSeesTheShippedDocPr(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow(nil, nil)
	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	require.NotNil(t, result.PR)

	rerun, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/main"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: result.PR.Number, Base: "main"},
		},
	})
	require.NoError(t, err)
	assert.True(t, rerun.ShouldRerun)
	assert.Contains(t, rerun.Reason, "#"+strconv.Itoa(result.PR.Number))

	// A feature-branch push does not gate a re-run of the default-based PR.
	feature, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
		Event:         gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: "refs/heads/feature/payments"},
		DefaultBranch: "main",
		OpenDocsyncPrs: []gitpr.OpenDocsyncPr{
			{Number: result.PR.Number, Base: "main"},
		},
	})
	require.NoError(t, err)
	assert.False(t, feature.ShouldRerun)
}

/* ------------------------------------------------------ fail-closed surfaces */

// TestCreateDocPrFlow_DepsValidationIsFailClosed pins the deps validation: a
// malformed wiring fails loud at composition (INV-8), never at run time.
func TestCreateDocPrFlow_DepsValidationIsFailClosed(t *testing.T) {
	f := fprMakeFixture(t)
	valid := gitpr.DocPrFlowDeps{
		Git:           f.git,
		Platform:      f.platform,
		Credentials:   gitpr.PrCredentials{Kind: gitpr.PrCredentialsDeveloper, Token: "test-token"},
		Repo:          fprRepoSlug,
		DefaultBranch: "main",
		Store:         f.store,
		Incidents:     gitpr.CreateIncidentLog(f.sink),
		Clock:         fprClock,
		Apply:         func(string, string) error { return nil },
	}
	cases := []struct {
		name   string
		broken func(*gitpr.DocPrFlowDeps)
	}{
		{"nil git", func(d *gitpr.DocPrFlowDeps) { d.Git = nil }},
		{"nil platform", func(d *gitpr.DocPrFlowDeps) { d.Platform = nil }},
		{"empty repo", func(d *gitpr.DocPrFlowDeps) { d.Repo = "" }},
		{"empty default branch", func(d *gitpr.DocPrFlowDeps) { d.DefaultBranch = "" }},
		{"nil store", func(d *gitpr.DocPrFlowDeps) { d.Store = nil }},
		{"nil incidents", func(d *gitpr.DocPrFlowDeps) { d.Incidents = nil }},
		{"nil clock", func(d *gitpr.DocPrFlowDeps) { d.Clock = nil }},
		{"nil apply", func(d *gitpr.DocPrFlowDeps) { d.Apply = nil }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			deps := valid
			testCase.broken(&deps)
			_, err := gitpr.CreateDocPrFlow(deps)
			require.Error(t, err)
			assert.Equal(t, gitpr.EDOCSYNCDocPrFlowConfig, docutil.CodeOf(err))
		})
	}

	// Half credentials are refused at composition too (INV-8).
	halfCreds := valid
	halfCreds.Credentials = gitpr.PrCredentials{Kind: gitpr.PrCredentialsAppInstallation, InstallationID: "1"}
	_, err := gitpr.CreateDocPrFlow(halfCreds)
	require.Error(t, err)
	assert.Equal(t, gitpr.CodePrCredentials, docutil.CodeOf(err))
}

// TestRun_InputValidationIsFailClosed pins the per-run input validation: a
// malformed input is a typed refusal (EDOCSYNC_DOC_PR_FLOW_INPUT), never a
// partial run.
func TestRun_InputValidationIsFailClosed(t *testing.T) {
	f := fprMakeFixture(t)
	flow := f.makeFlow()

	cases := []struct {
		name   string
		broken func(*gitpr.DocPrFlowInput)
	}{
		{"empty run id", func(in *gitpr.DocPrFlowInput) { in.RunID = "" }},
		{"nil manifest", func(in *gitpr.DocPrFlowInput) { in.Manifest = nil }},
		{"empty run head sha", func(in *gitpr.DocPrFlowInput) { in.RunHeadSha = fxStrPtr("") }},
		{"empty provenance run id", func(in *gitpr.DocPrFlowInput) {
			in.Provenance = &gitpr.FlowProvenance{RunID: ""}
		}},
		{"empty provenance record run id", func(in *gitpr.DocPrFlowInput) {
			empty := *f.record
			empty.RunID = ""
			in.ProvenanceRecord = &empty
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := f.makeInput()
			testCase.broken(&input)
			_, err := flow.Run(context.Background(), input)
			require.Error(t, err)
			assert.Equal(t, gitpr.EDOCSYNCDocPrFlowInput, docutil.CodeOf(err))
		})
	}
}

/* ------------------------------------------------- the M3a rebase abstention */

// TestM3a_RebaseConflictAbstainsNeverForceApplies ports M3a: a REAL rebase
// conflict abstains typed `rebase-conflict` — NEVER a forced apply (A9) — and
// the abort happens before any platform interaction.
func TestM3a_RebaseConflictAbstainsNeverForceApplies(t *testing.T) {
	f := fprMakeFixture(t)
	// Upstream rewrote the SAME line the flow's doc commit rewrites → the
	// replay onto the current default head CONFLICTS.
	f.repo.git(t, "checkout", "main")
	f.repo.write(t, "docs/payments.md",
		"# payments\n\nRefunds require an idempotency key (upstream rewrote this line).\n")
	f.repo.commitAll(t, "upstream rewrote the refund line")

	result := f.runFlow(nil, nil)

	fprAssertAborted(t, result, gitpr.PrFlowAbortRebaseConflict)
	assert.Contains(t, result.Detail, "rebasing the doc branch")
	assert.Contains(t, result.Detail, "A9")
	// Nothing was force-applied and no platform write ever happened.
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
	// The branch and tree were restored byte-identical by the git client's
	// abort — the branch STAYS for forensics (no cleanup, ever).
	assert.True(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
}

// TestFlow_PatchOutsideTheFrozenOutcomeIsIncoherent ports the step-3 evidence
// gap: a patch whose doc_path has NO frozen-outcome row is a caller bug — the
// flow consumes pre-derived evidence and never invents a row (fail closed).
func TestFlow_PatchOutsideTheFrozenOutcomeIsIncoherent(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.Patches = append(input.Patches, fprMakePatch("docs/orphan.md"))
	input.DocContents["docs/orphan.md"] = "# orphan\n"

	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortInconsistentInput)
	assert.Contains(t, result.Detail, "docs/orphan.md")
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
}

// TestP6T19_SelfTriggerSuppressionRendersTheHeadBranch pins both self-trigger
// projection paths: a docsync/* head branch names itself in the detail, and a
// missing head branch renders the `<unknown>` placeholder (P6-T19).
func TestP6T19_SelfTriggerSuppressionRendersTheHeadBranch(t *testing.T) {
	branchHeads := map[string]*string{
		"docsync head branch": fxStrPtr("docsync/pr-481-abc1234"),
		"absent head branch":  nil,
	}
	for name, headBranch := range branchHeads {
		t.Run(name, func(t *testing.T) {
			f := fprMakeFixture(t)
			input := f.makeInput()
			input.SourcePr = &gitpr.FlowSourcePr{
				Number:     fprSourcePr,
				HeadSha:    f.headSha,
				HeadBranch: headBranch,
				Author:     fxStrPtr("docsync-bot"),
			}
			result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
				deps.OwnIdentity = "docsync-bot"
			}}, &input)

			fprAssertAborted(t, result, gitpr.PrFlowAbortSelfTriggered)
			if headBranch != nil {
				assert.Contains(t, result.Detail, *headBranch)
			} else {
				assert.Contains(t, result.Detail, "<unknown>")
			}
			// Loop prevention is not a safety violation: no incident, and the
			// suppression precedes EVERY platform interaction.
			assert.Empty(t, f.sink.recorded)
			assert.Empty(t, f.platform.calls)
		})
	}
}

/* ------------------------------------------- the OD-R50 refusal classifier */

// TestODR50_AuthorityConfigSurfaceIncidentIsReclassified pins the single
// refusal→incident choke point (OD-R50): a refused path that targets an
// authority-config surface is classified as MODEL INFLUENCE on the system's
// permissions — the incident rule is reclassified and the detail names the
// surface, while the abort violation still carries the guard's own rule.
func TestODR50_AuthorityConfigSurfaceIncidentIsReclassified(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.Patches = []schemas.DocPatch{fprMakePatch(".docsync/manifest.yml")}
	input.StateUpdates = nil
	input.DocContents = map[string]string{".docsync/manifest.yml": "version: 1\ndocuments: []\n"}
	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortGuardViolation)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "manifest_or_policy_edit", result.Violations[0].Rule)
	assert.Equal(t, ".docsync/manifest.yml", result.Violations[0].Path)

	// The INCIDENT is the reclassified one (model influence on authority).
	require.Len(t, f.sink.recorded, 1)
	assert.Equal(t, schemas.SafetyIncidentRuleModelInfluenceOnAuthority, f.sink.recorded[0].Rule)
	assert.Contains(t, f.sink.recorded[0].Detail, "authority-config surface")
	assert.Equal(t, ".docsync/manifest.yml", f.sink.recorded[0].OffendingPath)
	// No platform call, no branch — the guard precedes every side effect.
	assert.Empty(t, f.platform.calls)
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
}

/* ------------------------------------------ the reverify gate's abstention */

// TestM3_ReverifyFailedAbstainsTreeUntouched ports the M3c abstention: merged
// content that does NOT re-pass verification abstains typed `reverify-failed`
// — never force-applied (A9).
func TestM3_ReverifyFailedAbstainsTreeUntouched(t *testing.T) {
	f := fprMakeFixture(t)
	// The default branch ADVANCES past the analysis anchor → the re-verify gate
	// runs and the injected hook FAILS the merged content.
	f.repo.write(t, "README.md", "# widget — upstream moved\n")
	f.repo.commitAll(t, "upstream advance")

	reverifyCalls := 0
	reverify := func(patch *schemas.DocPatch, _ string) *schemas.VerificationReport {
		reverifyCalls++
		return &schemas.VerificationReport{
			Schema:           "docsync.verification-report/v1",
			RunID:            fprRunID,
			DocPath:          patch.DocPath,
			PatchRef:         "patch-ref",
			Verdict:          schemas.VerdictReject,
			AutoEligible:     false,
			AutoBlockReasons: []string{schemas.AutoBlockUnanchoredClaims},
			PolicyVersion:    17,
		}
	}

	input := f.makeInput()
	input.SourcePr = &gitpr.FlowSourcePr{Number: fprSourcePr, HeadSha: f.headSha}
	input.RunHeadSha = fxStrPtr(f.headSha)
	result := f.runFlow([]fxDepOverride{func(deps *gitpr.DocPrFlowDeps) {
		deps.Reverify = reverify
	}}, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortReverifyFailed)
	assert.Contains(t, result.Detail, "did not re-pass verification")
	assert.Contains(t, result.Detail, "reject")
	// The verdict + block reasons are the evidence; nothing shipped.
	assert.Positive(t, reverifyCalls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))
	assert.NotContains(t, f.platform.calls, "createPr")
	assert.NotContains(t, f.platform.calls, "updatePr")
}

/* ------------------------------------------------- non-candidate outcome rows */

// TestFlow_NonCandidateOutcomeRowsNeverRideTheReport pins the candidate view's
// floor: an outcome row that is neither auto nor suggest is not a partial-pass
// candidate at all (never included, never reported as dropped).
func TestFlow_NonCandidateOutcomeRowsNeverRideTheReport(t *testing.T) {
	f := fprMakeFixture(t)
	outcome := fprMakeOutcome()
	confident := 0.95
	ignoreRouting := schemas.PipelineRoutingIgnore
	outcome.Documents = append(outcome.Documents, schemas.RunDocument{
		DocPath:         "docs/ignored.md",
		PolicyOutcome:   schemas.PipelineOutcomeIgnore,
		Confidence:      &confident,
		Routing:         &ignoreRouting,
		Verification:    &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass, AutoEligible: true, AutoBlockReasons: []string{}},
		PartialCoverage: false,
	})
	input := f.makeInput()
	input.Outcome = outcome

	result := f.runFlow(nil, &input)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	require.NotNil(t, result.PartialPass)
	// Only the three auto/suggest candidates ride the report — the
	// ignore-routed row never enters the partial-pass view at all.
	require.Len(t, result.PartialPass.Included, len(fprDocPaths))
	for _, included := range result.PartialPass.Included {
		assert.NotEqual(t, "docs/ignored.md", included.DocPath)
	}
	assert.Empty(t, result.PartialPass.Dropped)
}

/* --------------------------------------- the composed body's full evidence */

// TestV_P6_19_ComposedBodyRendersTheFullCanonicalEvidence pins the composed
// body surface through the flow: the abstains/findings/degradations sections
// and the D7 unknown-coverage note render the report's own evidence (consumed,
// never re-derived) alongside the provenance block.
func TestV_P6_19_ComposedBodyRendersTheFullCanonicalEvidence(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	abstainDocPath := "docs/abstain.md"
	findingDocPath := "docs/findings.md"
	generatedAt := fprClock()
	input.Canonical = schemas.CanonicalReport{
		RunID:         fprRunID,
		Key:           input.Outcome.Key,
		Status:        schemas.RunStatusCompleted,
		GeneratedAt:   &generatedAt,
		PolicyVersion: 17,
		ModelBindings: input.Outcome.Provenance.ModelBindings,
		Rows:          input.Canonical.Rows,
		Abstains: []schemas.RunAbstain{{
			AbstainID: "abs_01J9ZK7Q2M",
			DocPath:   &abstainDocPath,
			Reason:    "insufficient_confidence",
			Detail:    "documentation_impact probability 0.66 sits below the review threshold",
		}},
		Findings: []schemas.RunFinding{{
			DocPath: &findingDocPath,
			Kind:    "stale-example",
			Detail:  "code block references the removed `--legacy` flag",
		}},
		Degradations: []schemas.Degradation{{
			Stage:  "writer",
			Kind:   schemas.DegradationWriterTimeout,
			Detail: "one writer attempt timed out and was retried",
		}},
		UnknownCoverage: []string{"src/unlisted/**"},
	}

	result := f.runFlow(nil, &input)
	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)

	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 1)
	body := prs[0].Body

	// (e) the canonical evidence sections — explicit, never silenced.
	assert.Contains(t, body, "## Abstains")
	assert.Contains(t, body, "`abs_01J9ZK7Q2M` docs/abstain.md — insufficient_confidence")
	assert.Contains(t, body, "## Findings")
	assert.Contains(t, body, "docs/findings.md — stale-example")
	assert.Contains(t, body, "## Degradations")
	assert.Contains(t, body, "writer — writer-timeout — one writer attempt timed out and was retried")
	// (f) the D7 unknown-coverage note, only because coverage data exists.
	assert.Contains(t, body, "## Unknown coverage (D7)")
	assert.Contains(t, body, "- src/unlisted/**")
	// The provenance block still rides (composed, never replaced).
	assert.Contains(t, body, "## Provenance")
	assert.Contains(t, body, "- Run: `"+fprRunID+"`")
}
