package gitpr_test

// Ported from packages/core/test/doc-pr-provenance.test.ts (P9-T06 / V-P9-2 /
// V-P9-5): the provenance-record gate on the composed doc-PR flow and the D5
// canonical surface. The flow refuses to create or update a doc PR without the
// run's `docsync.provenance/v1` record (INV-10: a provenance-less PR is a bug,
// not a format option — typed refusal through the SAME ProvenanceMissingError
// path + `provenance_missing` incident, NO PR write), and the composed PR body
// renders the record via provenance.RenderProvenanceSummary while the COMMITTED
// doc file bytes never carry any provenance block (D5 rule 5 — the PR body is
// the only markdown surface). Real system git in a fresh temp repo (the
// gitpr-flow fixture pattern); the platform is the in-memory double behind a
// call-counting wrapper; the incident sink is a recording double. Constant ISO
// clock: no wall clock anywhere (P19).
//
// Helper prefix `fpr` (flow-provenance): the `gp` prefix is taken by the
// merge-order/filter suites (gpClock, gpGitRepo, …) and the shared
// repo/git/yaml fixtures are REUSED from filter_test.go (same test package).

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/orchestrator"
	"github.com/DUDE616/docSync-go/internal/provenance"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- constants */

const (
	// fprRepoSlug / fprRunID / fprSourcePr are the TS fixture constants.
	fprRepoSlug  = "acme/widgets"
	fprRunID     = "run_flow_01J"
	fprSourcePr  = 481
	fprStatePath = ".docsync/state/runs/run_flow_01J.json"
)

// fprDocPaths is the fixture's three documents (TS DOC_PATHS).
var fprDocPaths = []string{"docs/payments.md", "docs/api.md", "docs/cli.md"}

// fprClock is the constant ISO clock (P19: the burst limiter derives nowMs
// from it — a non-ISO value must latch a config error, so it stays ISO).
func fprClock() string { return "2026-10-07T00:00:00.000Z" }

/* ----------------------------------------------------------------- fixtures */

// fprMakePatch is the TS makePatch: a deterministic DocPatch clone with the
// run id and a distinct doc path per argument.
func fprMakePatch(docPath string) schemas.DocPatch {
	patch := schemas.DocPatchExample
	patch.RunID = fprRunID
	patch.DocPath = docPath
	return patch
}

// fprModePtr is the optional-mode pointer helper (TS `mode?`).
func fprModePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }

// fprMakeManifest is the TS makeManifest: every fixture doc is machine-writable
// (auto/suggest).
func fprMakeManifest() *schemas.Manifest {
	return &schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/payments.md", Mode: fprModePtr(schemas.DocModeAuto), Sources: []string{"src/payments/**"}},
			{Path: "docs/api.md", Mode: fprModePtr(schemas.DocModeSuggest), Sources: []string{"src/**"}},
			{Path: "docs/cli.md", Mode: fprModePtr(schemas.DocModeSuggest), Sources: []string{"src/cli/**"}},
		},
	}
}

// fprMakeOutcome is the TS makeVerifiedOutcome: a completed outcome whose three
// doc rows all carry verification PASS.
func fprMakeOutcome() schemas.RunOutcome {
	outcome := schemas.RunOutcomeExample()
	outcome.RunID = fprRunID
	continueRouting := schemas.PipelineRoutingContinue
	exactSHA := schemas.BaselineRunExactSHA
	outcome.Documents = make([]schemas.RunDocument, len(fprDocPaths))
	for index, docPath := range fprDocPaths {
		outcomeValue := schemas.PipelineOutcomeSuggest
		if index == 0 {
			outcomeValue = schemas.PipelineOutcomeAuto
		}
		confidence := 0.9
		outcome.Documents[index] = schemas.RunDocument{
			DocPath:         docPath,
			PolicyOutcome:   outcomeValue,
			Confidence:      &confidence,
			Routing:         &continueRouting,
			Verification:    &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass, AutoEligible: true, AutoBlockReasons: []string{}},
			PartialCoverage: false,
			BaselineRun:     &exactSHA,
		}
	}
	return outcome
}

// fprFlowBindings projects the outcome's routed-model bindings onto the flow's
// provenance evidence (TS `modelBindings: outcome.provenance.model_bindings`).
func fprFlowBindings(bindings []schemas.RunModelBinding) []gitpr.FlowModelBinding {
	out := make([]gitpr.FlowModelBinding, len(bindings))
	for i, binding := range bindings {
		out[i] = gitpr.FlowModelBinding{Role: binding.Role, Model: binding.Model, Provider: binding.Provider}
	}
	return out
}

// fxDepOverride is one DocPrFlowDeps override (TS makeFlow overrides) — an
// ALIAS so both flow test files pass overrides interchangeably.
type fxDepOverride = func(*gitpr.DocPrFlowDeps)

// fprRecordingSink is the recording IncidentSink double (in-memory,
// order-preserving; TS RecordingSink).
type fprRecordingSink struct {
	recorded []schemas.SafetyIncident
}

func (s *fprRecordingSink) Append(_ context.Context, incident *schemas.SafetyIncident) error {
	s.recorded = append(s.recorded, *incident)
	return nil
}

// fprCountingPlatform is the call-counting PrPlatformPort wrapper (TS
// CountingPlatform) — the zero-platform-call assertions ride on it.
type fprCountingPlatform struct {
	inner gitpr.PrPlatformPort
	calls []string
}

func (p *fprCountingPlatform) record(op string) { p.calls = append(p.calls, op) }

func (p *fprCountingPlatform) ListOpenDocsPrs(ctx context.Context, repo string) ([]gitpr.PrRef, error) {
	p.record("listOpenDocsPrs")
	return p.inner.ListOpenDocsPrs(ctx, repo)
}

func (p *fprCountingPlatform) FindByPrBodyMarker(
	ctx context.Context, repo, marker string, opts *gitpr.PrMarkerLookupOptions,
) (gitpr.PrRef, bool, error) {
	p.record("findByPrBodyMarker")
	return p.inner.FindByPrBodyMarker(ctx, repo, marker, opts)
}

func (p *fprCountingPlatform) CreatePr(ctx context.Context, input gitpr.CreatePrInput) (gitpr.PrRef, error) {
	p.record("createPr")
	return p.inner.CreatePr(ctx, input)
}

func (p *fprCountingPlatform) UpdatePr(
	ctx context.Context, pr gitpr.PrRef, input gitpr.UpdatePrInput,
) (gitpr.PrRef, error) {
	p.record("updatePr")
	return p.inner.UpdatePr(ctx, pr, input)
}

func (p *fprCountingPlatform) MarkSuperseded(ctx context.Context, pr, newerPr gitpr.PrRef) error {
	p.record("markSuperseded")
	return p.inner.MarkSuperseded(ctx, pr, newerPr)
}

func (p *fprCountingPlatform) ClosePr(ctx context.Context, pr gitpr.PrRef, comment string) error {
	p.record("closePr")
	return p.inner.ClosePr(ctx, pr, comment)
}

func (p *fprCountingPlatform) AppendPrComment(ctx context.Context, pr gitpr.PrRef, body string) error {
	p.record("appendPrComment")
	return p.inner.AppendPrComment(ctx, pr, body)
}

// fprFixture is the TS FlowFixture: temp repo + real GitClient + in-memory
// platform + recording sink + the run's provenance record.
type fprFixture struct {
	t        *testing.T
	repo     *gpGitRepo
	git      *gitx.GitClient
	platform *fprCountingPlatform
	inner    *gitpr.InMemoryPrPlatform
	sink     *fprRecordingSink
	store    *testkit.InMemoryStateStore
	headSha  string
	record   *schemas.Provenance
}

// fprMakeFixture is the TS makeFixture: a real temp repository, the keyed
// in-memory platform double, the recording incident sink and the run's
// `docsync.provenance/v1` record. The committed `.docsync/state/.gitignore`
// (ignoring `merge-ledger/`) keeps the flow's ledger bookkeeping from ever
// presenting as a dirty tree to a follow-up run's pre-flight.
func fprMakeFixture(t *testing.T) *fprFixture {
	t.Helper()
	repo := gpNewFilterRepo(t)
	repo.write(t, "README.md", "# widget\n")
	repo.write(t, "docs/payments.md", "# payments\n\nRefunds require an idempotency key.\n")
	repo.write(t, "docs/api.md", "# api\n\nEndpoint list lives here.\n")
	repo.write(t, "docs/cli.md", "# cli\n\nCommands live here.\n")
	repo.write(t, ".docsync/state/.gitignore", "merge-ledger/\n")
	headSha := repo.commitAll(t, "initial")
	sink := &fprRecordingSink{}
	inner := gitpr.NewInMemoryPrPlatform(&gitpr.InMemoryPrPlatformOptions{Clock: fprClock})
	store := testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{})
	record, err := provenance.BuildProvenanceRecord(provenance.BuildProvenanceInput{
		RunID:              fprRunID,
		SourceCommit:       headSha,
		SourceFiles:        []string{"src/payments/refund.ts"},
		RoutedModels:       provenance.RoutedModels{DetectorModel: "z-ai/glm-4.7", WriterModel: "anthropic/claude-sonnet-4.5"},
		DetectorConfidence: 0.9,
		VerificationStatus: "pass",
		PolicyVersion:      17,
		ManifestVersion:    "1",
		Now:                fprClock(),
	})
	require.NoError(t, err)
	return &fprFixture{
		t:        t,
		repo:     repo,
		git:      repo.client(t),
		platform: &fprCountingPlatform{inner: inner},
		inner:    inner,
		sink:     sink,
		store:    store,
		headSha:  headSha,
		record:   record,
	}
}

// makeInput is the TS makeInput(): a fresh, fully-verified flow input; tests
// mutate the returned struct for their overrides (Go has no Partial<T>).
func (f *fprFixture) makeInput() gitpr.DocPrFlowInput {
	outcome := fprMakeOutcome()
	canonical, err := orchestrator.BuildCanonicalReport(&outcome)
	require.NoError(f.t, err)
	canonical.RunID = fprRunID
	return gitpr.DocPrFlowInput{
		RunID:    fprRunID,
		SourcePr: &gitpr.FlowSourcePr{Number: fprSourcePr, HeadSha: f.headSha},
		Patches: []schemas.DocPatch{
			fprMakePatch("docs/payments.md"),
			fprMakePatch("docs/api.md"),
			fprMakePatch("docs/cli.md"),
		},
		StateUpdates: []gitpr.CommitStateUpdate{
			{RelPath: fprStatePath, Content: `{"run_id":"` + fprRunID + `"}` + "\n"},
		},
		Provenance: &gitpr.FlowProvenance{
			RunID:         fprRunID,
			PolicyVersion: outcome.Provenance.PolicyVersion,
			ModelBindings: fprFlowBindings(outcome.Provenance.ModelBindings),
		},
		ProvenanceRecord: f.record,
		Outcome:          outcome,
		Canonical:        *canonical,
		Manifest:         fprMakeManifest(),
		DocContents: map[string]string{
			"docs/payments.md": "# payments\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
			"docs/api.md":      "# api\n\nNew endpoint documented.\n",
			"docs/cli.md":      "# cli\n\nNew command documented.\n",
		},
	}
}

// makeFlow is the TS makeFlow: the composed flow over the fixture with the
// shared deps; overrides (a reverify hook, a racy store, …) win.
func (f *fprFixture) makeFlow(overrides ...func(*gitpr.DocPrFlowDeps)) gitpr.DocPrFlow {
	deps := gitpr.DocPrFlowDeps{
		Git:      f.git,
		Platform: f.platform,
		Credentials: gitpr.PrCredentials{
			Kind:  gitpr.PrCredentialsDeveloper,
			Token: "test-token",
		},
		Repo:          fprRepoSlug,
		DefaultBranch: "main",
		Store:         f.store,
		Incidents:     gitpr.CreateIncidentLog(f.sink),
		Clock:         fprClock,
		Apply: func(path, content string) error {
			f.repo.write(f.t, path, content)
			return nil
		},
	}
	for _, override := range overrides {
		override(&deps)
	}
	flow, err := gitpr.CreateDocPrFlow(deps)
	require.NoError(f.t, err)
	return flow
}

// runFlow is the TS runFlow: one composed-flow run with optional dep overrides
// and an optional pre-built input.
func (f *fprFixture) runFlow(
	overrides []func(*gitpr.DocPrFlowDeps),
	input *gitpr.DocPrFlowInput,
) gitpr.PrFlowResult {
	if input == nil {
		built := f.makeInput()
		input = &built
	}
	result, err := f.makeFlow(overrides...).Run(context.Background(), *input)
	require.NoError(f.t, err)
	return result
}

// branchExists is the TS branchExists helper.
func (f *fprFixture) branchExists(branch string) bool {
	_, err := f.git.ResolveRev(context.Background(), "refs/heads/"+branch)
	return err == nil
}

// loadLedger is the TS loadLedger: the merge-order ledger for the given source
// key, read through the SAME store the flow wrote through.
func (f *fprFixture) loadLedger(sourceKey string) *schemas.MergeLedger {
	ledger, err := gitpr.NewMergeLedgerStore(gitpr.MergeLedgerStoreDeps{Store: f.store})
	require.NoError(f.t, err)
	loaded, err := ledger.Load(sourceKey)
	require.NoError(f.t, err)
	return loaded
}

// localBranches is the TS localBranches helper (for the M5 branch-count
// invariance assertion).
func (f *fprFixture) localBranches() []string {
	out := f.repo.git(f.t, "for-each-ref", "refs/heads", "--format=%(refname)")
	branches := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			branches = append(branches, trimmed)
		}
	}
	return branches
}

// docSetHash renders the doc paths of a branch diff against main (the TS
// `git diff --name-only main <branch>` projection).
func (f *fprFixture) diffAgainstMain(branch string) []string {
	out := f.repo.git(f.t, "diff", "--name-only", "main", branch)
	changed := make([]string, 0)
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			changed = append(changed, trimmed)
		}
	}
	sort.Strings(changed)
	return changed
}

// fprAssertAborted pins the aborted kind + reason (TS assertAborted).
func fprAssertAborted(t *testing.T, result gitpr.PrFlowResult, reason gitpr.PrFlowAbortReason) {
	t.Helper()
	require.Equal(t, gitpr.PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(reason), result.Reason)
}

/* ------------------------------------------------------------------ tests */

// TestV_P9_2_DocPrCreationWithoutProvenanceRecordIsRefused ports V-P9-2: doc-PR
// creation without a provenance record is refused with a provenance-missing
// error and a logged incident (INV-10) — fail closed, NO PR write, NO branch.
func TestV_P9_2_DocPrCreationWithoutProvenanceRecordIsRefused(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	input.ProvenanceRecord = nil
	result := f.runFlow(nil, &input)

	// Typed refusal: the flow aborts `provenance-missing` with the
	// ProvenanceMissingError detail — fail closed, NO PR write.
	fprAssertAborted(t, result, gitpr.PrFlowAbortProvenanceMissing)
	assert.Contains(t, result.Detail, "provenance record missing or empty")
	assert.Empty(t, result.Violations)

	// ZERO platform calls — the gate precedes every platform interaction.
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.inner.Prs(fprRepoSlug))

	// The incident was recorded through the existing safety-incident path.
	require.Len(t, f.sink.recorded, 1)
	incident := f.sink.recorded[0]
	assert.Equal(t, schemas.SafetyIncidentRuleProvenanceMissing, incident.Rule)
	assert.Equal(t, fprRunID, incident.RunID)
	assert.Equal(t, fprDocPaths[0], incident.OffendingPath)
	require.NotNil(t, incident.SourcePRNumber)
	assert.Equal(t, fprSourcePr, *incident.SourcePRNumber)

	// No branch was created either (the gate precedes ALL side effects).
	assert.False(t, f.branchExists("docsync/pr-481-"+f.headSha[:7]))
}

// TestV_P9_5_DocPrFileContentsCarryNoProvenanceBlocks ports V-P9-5: doc-PR file
// contents contain no provenance blocks (D5 rule 5) — the PR body is the ONLY
// markdown surface for provenance.
func TestV_P9_5_DocPrFileContentsCarryNoProvenanceBlocks(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow(nil, nil)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	branch := result.Branch
	require.NotEmpty(t, branch)

	// The COMMITTED doc file bytes never carry the provenance schema tag or
	// any block marker (banner / headings) — D5 rule 5.
	for _, docPath := range fprDocPaths {
		committed := f.repo.git(t, "show", branch+":"+docPath)
		assert.NotContains(t, committed, f.record.Schema)
		assert.NotContains(t, committed, provenance.PrBodyBanner)
		assert.NotContains(t, committed, "## Provenance")
		assert.NotContains(t, committed, "## Documentation mapping")
		assert.NotContains(t, committed, "## Verification")
	}
	// The committed state carriage is equally free of provenance blocks.
	committedState := f.repo.git(t, "show", branch+":"+fprStatePath)
	assert.NotContains(t, committedState, f.record.Schema)
	assert.NotContains(t, committedState, provenance.PrBodyBanner)

	// …while the PR body DOES contain the block (the record is provided).
	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 1)
	body := prs[0].Body
	assert.Contains(t, body, provenance.PrBodyBanner)
	assert.Contains(t, body, "## Provenance")
	assert.Contains(t, body, "- Run: `"+fprRunID+"`")
	assert.Contains(t, body, "## Documentation mapping")
	assert.Contains(t, body, "## Verification")
	assert.Contains(t, body, "- Status: `pass`")
}

// TestV_P9_5_ComposedDocPrBodyCarriesTheCanonicalRecord ports V-P9-5: the
// composed doc-PR body carries the canonical provenance record block (D5).
func TestV_P9_5_ComposedDocPrBodyCarriesTheCanonicalRecord(t *testing.T) {
	f := fprMakeFixture(t)
	result := f.runFlow(nil, nil)

	require.Equal(t, gitpr.PrFlowKindCreated, result.Kind)
	prs := f.inner.Prs(fprRepoSlug)
	require.Len(t, prs, 1)
	body := prs[0].Body

	// banner (single source: PrBodyBanner, imported — never re-declared)
	assert.True(t, strings.HasPrefix(body, provenance.PrBodyBanner+"\n"))

	// provenance ids: run id, policy version, manifest version, routed
	// models with the detector confidence, and the record's generated_at.
	assert.Contains(t, body, "- Run: `"+fprRunID+"`")
	assert.Contains(t, body, "- Policy: v17")
	assert.Contains(t, body, "- Manifest: v1")
	assert.Contains(t, body,
		"- Models: detector `z-ai/glm-4.7` (confidence 0.90) · writer `anthropic/claude-sonnet-4.5`")
	assert.Contains(t, body, "- Generated: "+fprClock())

	// candidate mapping table over the record's source files …
	assert.Contains(t, body, "## Documentation mapping")
	assert.Contains(t, body, "| Source file |")
	assert.Contains(t, body, "| src/payments/refund.ts |")

	// …and the verification status in the canonical verdict vocabulary.
	assert.Contains(t, body, "## Verification")
	assert.Contains(t, body, "- Status: `pass`")

	// The Phase-6 sections still ride (the body is composed, not replaced).
	assert.Contains(t, body, "- Source PR: #481")
	assert.Contains(t, body, "## Partial pass (D4)")
	assert.Contains(t, body, "<!-- docsync:idempotency:"+result.IdempotencyKey+" -->")
}

// TestP9T06_ProvenanceRecordNamingAnotherRunIsIncoherent ports P9-T06: a
// provenance record naming another run is refused as incoherent input — a
// caller bug (typed refusal, NO incident: it is not a safety violation).
func TestP9T06_ProvenanceRecordNamingAnotherRunIsIncoherent(t *testing.T) {
	f := fprMakeFixture(t)
	input := f.makeInput()
	foreign := *f.record
	foreign.RunID = "run_other_01J"
	input.ProvenanceRecord = &foreign
	result := f.runFlow(nil, &input)

	fprAssertAborted(t, result, gitpr.PrFlowAbortInconsistentInput)
	assert.Contains(t, result.Detail, "run_other_01J")
	assert.Empty(t, f.platform.calls)
	assert.Empty(t, f.sink.recorded)
}
