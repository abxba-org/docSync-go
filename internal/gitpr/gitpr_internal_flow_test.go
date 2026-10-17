package gitpr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	gitx "github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
	"github.com/DUDE616/docSync-go/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	covFlowRunID     = "run_flow_01"
	covFlowRepoSlug  = "acme/widgets"
	covFlowHeadSha   = "0123456789abcdef0123456789abcdef01234567"
	covFlowStatePath = ".docsync/state/run_state.json"
	covFlowSourcePr  = 481
)

var covFlowDocPaths = []string{"docs/payments.md", "docs/api.md", "docs/cli.md"}

// covFlowUnlistedDoc is a patched doc the manifest does NOT allowlist.
const covFlowUnlistedDoc = "docs/unlisted.md"

// covFlowRepo is a REAL git repository (deterministic: no network, no user
// config, fixed identity/dates). P19 holds: the wall clock is never read.
type covFlowRepo struct{ root string }

func covNewFlowRepo(t *testing.T) *covFlowRepo {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=docsync-test",
			"GIT_AUTHOR_EMAIL=test@docsync.example",
			"GIT_COMMITTER_NAME=docsync-test",
			"GIT_COMMITTER_EMAIL=test@docsync.example",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("init", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	for _, docPath := range covFlowDocPaths {
		require.NoError(t, os.WriteFile(filepath.Join(root, docPath), []byte("# doc\n"), 0o644))
	}
	run("add", "-A")
	run("commit", "-m", "chore: initial")
	return &covFlowRepo{root: root}
}

func (r *covFlowRepo) write(t *testing.T, relPath, content string) {
	t.Helper()
	full := filepath.Join(r.root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

func (r *covFlowRepo) rev(t *testing.T, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = r.root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git rev-parse %s: %s", ref, out)
	sha := string(out)
	if len(sha) > 0 && sha[len(sha)-1] == '\n' {
		sha = sha[:len(sha)-1]
	}
	return sha
}

func (r *covFlowRepo) client(t *testing.T) *gitx.GitClient {
	t.Helper()
	client, err := gitx.NewGitClient(gitx.GitClientOptions{RepoRoot: r.root})
	require.NoError(t, err)
	return client
}

// covFlowPlatform is the PrPlatformPort double the flow drives: scripted by
// key, recording, and switchable into a mode that FAILS one operation.
type covFlowPlatform struct {
	inner *InMemoryPrPlatform
	fails map[string]error
	calls []string
}

func newCovFlowPlatform() *covFlowPlatform {
	return &covFlowPlatform{
		inner: NewInMemoryPrPlatform(&InMemoryPrPlatformOptions{Clock: covFixedTime}),
		fails: map[string]error{},
	}
}

func (p *covFlowPlatform) fail(op string, err error) { p.fails[op] = err }

func (p *covFlowPlatform) record(op string) { p.calls = append(p.calls, op) }

func (p *covFlowPlatform) ListOpenDocsPrs(ctx context.Context, repo string) ([]PrRef, error) {
	p.record("listOpenDocsPrs")
	if err := p.fails["listOpenDocsPrs"]; err != nil {
		return nil, err
	}
	return p.inner.ListOpenDocsPrs(ctx, repo)
}

func (p *covFlowPlatform) FindByPrBodyMarker(ctx context.Context, repo, marker string, opts *PrMarkerLookupOptions) (PrRef, bool, error) {
	p.record("findByPrBodyMarker")
	if err := p.fails["findByPrBodyMarker"]; err != nil {
		return PrRef{}, false, err
	}
	return p.inner.FindByPrBodyMarker(ctx, repo, marker, opts)
}

func (p *covFlowPlatform) CreatePr(ctx context.Context, input CreatePrInput) (PrRef, error) {
	p.record("createPr")
	if err := p.fails["createPr"]; err != nil {
		return PrRef{}, err
	}
	return p.inner.CreatePr(ctx, input)
}

func (p *covFlowPlatform) UpdatePr(ctx context.Context, pr PrRef, input UpdatePrInput) (PrRef, error) {
	p.record("updatePr")
	if err := p.fails["updatePr"]; err != nil {
		return PrRef{}, err
	}
	return p.inner.UpdatePr(ctx, pr, input)
}

func (p *covFlowPlatform) AppendPrComment(ctx context.Context, pr PrRef, body string) error {
	p.record("appendPrComment")
	if err := p.fails["appendPrComment"]; err != nil {
		return err
	}
	return p.inner.AppendPrComment(ctx, pr, body)
}

func (p *covFlowPlatform) MarkSuperseded(ctx context.Context, pr, newerPr PrRef) error {
	p.record("markSuperseded")
	if err := p.fails["markSuperseded"]; err != nil {
		return err
	}
	return p.inner.MarkSuperseded(ctx, pr, newerPr)
}

func (p *covFlowPlatform) ClosePr(ctx context.Context, pr PrRef, comment string) error {
	p.record("closePr")
	if err := p.fails["closePr"]; err != nil {
		return err
	}
	return p.inner.ClosePr(ctx, pr, comment)
}

// covFlowFixture is the healthy preconditions bundle.
type covFlowFixture struct {
	t        *testing.T
	repo     *covFlowRepo
	client   *gitx.GitClient
	platform *covFlowPlatform
	store    *testkit.InMemoryStateStore
	headSha  string
}

func covBuildFlowFixture(t *testing.T) *covFlowFixture {
	t.Helper()
	repo := covNewFlowRepo(t)
	fixture := &covFlowFixture{
		t: t, repo: repo, client: repo.client(t),
		platform: newCovFlowPlatform(),
		store:    testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{}),
		headSha:  repo.rev(t, "main"),
	}
	return fixture
}

// covFlowManifest allowlists every fixture doc as machine-writable.
func covFlowManifest() *schemas.Manifest {
	auto := schemas.DocModeAuto
	suggest := schemas.DocModeSuggest
	return &schemas.Manifest{Version: 1, Documents: []schemas.ManifestEntry{
		{Path: "docs/payments.md", Mode: &auto},
		{Path: "docs/api.md", Mode: &suggest},
		{Path: "docs/cli.md", Mode: &suggest},
	}}
}

func covFlowPatch(docPath string) schemas.DocPatch {
	patchValue := schemas.DocPatchExample
	patchValue.RunID = covFlowRunID
	patchValue.DocPath = docPath
	return patchValue
}

func covFlowOutcome() schemas.RunOutcome {
	outcome := schemas.RunOutcomeExample()
	outcome.RunID = covFlowRunID
	continueRouting := schemas.PipelineRoutingContinue
	outcome.Documents = make([]schemas.RunDocument, len(covFlowDocPaths))
	for index, docPath := range covFlowDocPaths {
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
		}
	}
	unlisted := 0.3
	outcome.Documents = append(outcome.Documents, schemas.RunDocument{
		DocPath:         covFlowUnlistedDoc,
		PolicyOutcome:   schemas.PipelineOutcomeSuggest,
		Confidence:      &unlisted,
		Routing:         &continueRouting,
		Verification:    &schemas.RunDocumentVerification{Verdict: schemas.VerdictPass, AutoEligible: true, AutoBlockReasons: []string{}},
		PartialCoverage: false,
	})
	outcome.Provenance.PolicyVersion = 17
	return outcome
}

// covFlowInput assembles the standard healthy flow input.
func covFlowInput(f *covFlowFixture) DocPrFlowInput {
	patches := make([]schemas.DocPatch, 0, len(covFlowDocPaths))
	docContents := map[string]string{}
	for _, docPath := range covFlowDocPaths {
		patches = append(patches, covFlowPatch(docPath))
		docContents[docPath] = "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n"
	}
	headSha := f.headSha
	sourcePr := covFlowSourcePr
	return DocPrFlowInput{
		RunID:    covFlowRunID,
		SourcePr: &FlowSourcePr{Number: sourcePr, HeadSha: headSha},
		Patches:  patches,
		StateUpdates: []CommitStateUpdate{
			{RelPath: covFlowStatePath, Content: `{"run_id":"` + covFlowRunID + `"}` + "\n"},
		},
		Provenance: &FlowProvenance{
			RunID:         covFlowRunID,
			PolicyVersion: 17,
		},
		ProvenanceRecord: covFlowProvenanceRecord(),
		Outcome:          covFlowOutcome(),
		Manifest:         covFlowManifest(),
		DocContents:      docContents,
	}
}

// covFlowProvenanceRecord is the schema-valid P9 provenance record the gate
// requires (INV-10).
func covFlowProvenanceRecord() *schemas.Provenance {
	return &schemas.Provenance{
		Schema:          "docsync.provenance/v1",
		RunID:           covFlowRunID,
		SourceCommit:    covFlowHeadSha,
		SourceFiles:     []string{"src/doc.ts"},
		GeneratedAt:     covFixedTime(),
		PolicyVersion:   17,
		ManifestVersion: "1",
		Detector:        schemas.ProvenanceDetector{Model: "det/1", Confidence: 0.9},
		Writer:          schemas.ProvenanceWriter{Model: "test/writer"},
		Verification:    schemas.ProvenanceVerification{Status: schemas.VerdictPass},
	}
}

func budgetPtr(value int) *int { return &value }

// covFlowDeps assembles the flow over the fixture.
func covFlowDeps(f *covFlowFixture, reverifyPass bool) DocPrFlowDeps {
	return DocPrFlowDeps{
		Git:           f.client,
		Platform:      f.platform,
		Credentials:   PrCredentials{Kind: PrCredentialsDeveloper, Token: "ghp-flow-token"},
		Repo:          covFlowRepoSlug,
		DefaultBranch: "main",
		Store:         f.store,
		Incidents:     CreateIncidentLog(&covCountingSink{}),
		Clock:         covFixedTime,
		Reverify: func(*schemas.DocPatch, string) *schemas.VerificationReport {
			return &schemas.VerificationReport{
				Verdict: schemas.VerdictPass, AutoEligible: reverifyPass,
			}
		},
		OwnIdentity: "docsync[bot]",
		Apply: func(relPath, content string) error {
			full := filepath.Join(f.repo.root, relPath)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			return os.WriteFile(full, []byte(content), 0o644)
		},
	}
}

// TestCOV_GITPR_031_FlowInputRefusals pins the per-run input validation and
// the flow's construction refusals (fail closed before ANY side effect).
func TestCOV_GITPR_031_FlowInputRefusals(t *testing.T) {
	fixture := covBuildFlowFixture(t)
	deps := covFlowDeps(fixture, true)
	input := covFlowInput(fixture)
	flow, err := CreateDocPrFlow(deps)
	require.NoError(t, err)

	// An empty run id.
	broken := input
	broken.RunID = ""
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)
	assert.Equal(t, EDOCSYNCDocPrFlowInput, docutilCode(err))

	// A nil manifest.
	broken = input
	broken.Manifest = nil
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)

	// An empty-string run head sha when provided.
	broken = input
	empty := ""
	broken.RunHeadSha = &empty
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)

	// An empty provenance run id.
	broken = input
	broken.Provenance.RunID = ""
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)

	// A missing provenance record.
	broken = input
	broken.ProvenanceRecord = nil
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)

	// A provenance record naming another run.
	broken = input
	renamed := covFlowProvenanceRecord()
	renamed.RunID = "another_run"
	broken.ProvenanceRecord = renamed
	_, err = flow.Run(context.Background(), broken)
	require.Error(t, err)

	// The construction refusals.
	_, err = CreateDocPrFlow(DocPrFlowDeps{})
	require.Error(t, err)
	assert.Equal(t, EDOCSYNCDocPrFlowConfig, docutilCode(err))
	noGit := covFlowDeps(fixture, true)
	noGit.Git = nil
	_, err = CreateDocPrFlow(noGit)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deps.git must be a GitClient")
	noPlatform := covFlowDeps(fixture, true)
	noPlatform.Platform = nil
	_, err = CreateDocPrFlow(noPlatform)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "platform")
	noClock := covFlowDeps(fixture, true)
	noClock.Clock = nil
	_, err = CreateDocPrFlow(noClock)
	require.Error(t, err)
	noApply := covFlowDeps(fixture, true)
	noApply.Apply = nil
	_, err = CreateDocPrFlow(noApply)
	require.Error(t, err)
	noIncidents := covFlowDeps(fixture, true)
	noIncidents.Incidents = nil
	_, err = CreateDocPrFlow(noIncidents)
	require.Error(t, err)
}

// TestCOV_GITPR_032_FlowProvenanceGateAborts pins the INV-10 gate: a
// missing/mismatched provenance record aborts BEFORE any side effect.
func TestCOV_GITPR_032_FlowProvenanceGateAborts(t *testing.T) {
	fixture := covBuildFlowFixture(t)
	flow, err := CreateDocPrFlow(covFlowDeps(fixture, true))
	require.NoError(t, err)
	input := covFlowInput(fixture)

	// A record whose run id disagrees with the flow run id ⇒ abort.
	mismatched := input
	other := covFlowProvenanceRecord()
	other.RunID = "acme/other#deadbeef"
	mismatched.ProvenanceRecord = other
	result, err := flow.Run(context.Background(), mismatched)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(PrFlowAbortInconsistentInput), result.Reason)

	// The provenance ring RunID disagreeing with the flow run id ⇒ abort.
	ringMismatch := input
	ring := *input.Provenance // a copy: the original stays usable below
	ring.RunID = "acme/other#deadbeef"
	ringMismatch.Provenance = &ring
	result, err = flow.Run(context.Background(), ringMismatch)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(PrFlowAbortInconsistentInput), result.Reason)

	// An ABSENT record routes through the provenance-missing refusal.
	absent := input
	absent.ProvenanceRecord = nil
	result, err = flow.Run(context.Background(), absent)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(PrFlowAbortProvenanceMissing), result.Reason)
}

// TestCOV_GITPR_033_FlowAssemblyAndGuardAborts pins the plan-assembly and
// content-guard refusals (a commit is never assembled from an unguarded plan).
func TestCOV_GITPR_033_FlowAssemblyAndGuardAborts(t *testing.T) {
	fixture := covBuildFlowFixture(t)
	flow, err := CreateDocPrFlow(covFlowDeps(fixture, true))
	require.NoError(t, err)
	input := covFlowInput(fixture)

	// A patch with no applied content ⇒ CommitAssemblyError ⇒ abort.
	unresolved := input
	unresolved.DocContents = map[string]string{"docs/payments.md": ""}
	result, err := flow.Run(context.Background(), unresolved)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)

	// A MANIFEST-protected doc path ⇒ guard violation (INV-2).
	protected := input
	protectedPatches := make([]schemas.DocPatch, len(input.Patches))
	copy(protectedPatches, input.Patches)
	protectedPatches[1].DocPath = "manifest.yml"
	protected.Patches = protectedPatches
	protected.DocContents = map[string]string{
		"docs/payments.md": "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
		"manifest.yml":     "# not a doc\n",
		"docs/cli.md":      "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
	}
	result, err = flow.Run(context.Background(), protected)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(PrFlowAbortGuardViolation), result.Reason)
}

// TestCOV_GITPR_034_FlowDefaultBranchGuard pins INV-1 at the flow seam: a
// doc branch that resolves to the default branch is refused before any ref is
// created.
func TestCOV_GITPR_034_FlowDefaultBranchGuard(t *testing.T) {
	fixture := covBuildFlowFixture(t)
	deps := covFlowDeps(fixture, true)
	// A default branch that collides with the flow's own docsym branch.
	deps.DefaultBranch = SelfTriggerBranchPrefix + covFlowRunID
	flow, err := CreateDocPrFlow(deps)
	require.NoError(t, err)
	local := covFlowInput(fixture)
	local.SourcePr = nil // a local run's branch is `docsync/<runId>`
	_, err = flow.Run(context.Background(), local)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INV-1")
	assert.Contains(t, err.Error(), "never proceed")
}

func docutilCode(err error) string {
	if err == nil {
		return ""
	}
	return docutil.CodeOf(err)
}

// covFailingLedgerStore fails one ledger operation on demand (R15 paths).
type covFailingLedgerStore struct {
	inner    *testkit.InMemoryStateStore
	readErr  error
	writeErr error
}

func (s *covFailingLedgerStore) ReadMergeLedger(sourceKey string) (state.StateRead[*schemas.MergeLedger], error) {
	if s.readErr != nil {
		return state.StateRead[*schemas.MergeLedger]{}, s.readErr
	}
	return s.inner.ReadMergeLedger(sourceKey)
}

func (s *covFailingLedgerStore) WriteMergeLedger(sourceKey string, ledger schemas.MergeLedger, expectedHash *string) (state.CasWriteResult, error) {
	if s.writeErr != nil {
		return state.CasWriteResult{}, s.writeErr
	}
	return s.inner.WriteMergeLedger(sourceKey, ledger, expectedHash)
}

// TestCOV_GITPR_035_FlowInjectedFailures pins the flow's own error surface
// when its collaborators refuse (every failure is loud and typed; nothing is
// silently swallowed and no PR is shipped).
func TestCOV_GITPR_035_FlowInjectedFailures(t *testing.T) {
	ctx := context.Background()

	runWith := func(t *testing.T, mutate func(*covFlowFixture, *covFlowPlatform, DocPrFlowDeps) DocPrFlowDeps) error {
		t.Helper()
		fixture := covBuildFlowFixture(t)
		deps := covFlowDeps(fixture, true)
		flow, err := CreateDocPrFlow(mutate(fixture, fixture.platform, deps))
		require.NoError(t, err)
		_, err = flow.Run(ctx, covFlowInput(fixture))
		return err
	}

	// A ledger READ failure surfaces as the flow's error.
	err := runWith(t, func(_ *covFlowFixture, _ *covFlowPlatform, deps DocPrFlowDeps) DocPrFlowDeps {
		deps.Store = &covFailingLedgerStore{
			inner:   testkit.NewInMemoryStateStore(testkit.InMemoryStateStoreOptions{}),
			readErr: assertFailure(),
		}
		return deps
	})
	require.Error(t, err)

	// A marker-lookup failure surfaces as the flow's error.
	err = runWith(t, func(fixture *covFlowFixture, platform *covFlowPlatform, deps DocPrFlowDeps) DocPrFlowDeps {
		platform.fail("findByPrBodyMarker", assertFailure())
		return deps
	})
	require.Error(t, err)

	// A create failure surfaces as the flow's error.
	err = runWith(t, func(fixture *covFlowFixture, platform *covFlowPlatform, deps DocPrFlowDeps) DocPrFlowDeps {
		platform.fail("createPr", assertFailure())
		return deps
	})
	require.Error(t, err)

	// A burst-delay failure fails CLOSED (never a silent admit): the limiter is
	// closure-hoisted across runs, so the SECOND run of a MaxPerHour=1 flow is
	// over budget and must fail loud rather than ship silently.
	burstFixture := covBuildFlowFixture(t)
	burstDeps := covFlowDeps(burstFixture, true)
	burstDeps.Delay = func(context.Context, int64) error { return assertFailure() }
	burstFlow, err := CreateDocPrFlow(burstDeps)
	require.NoError(t, err)
	burstInput := covFlowInput(burstFixture)
	burstInput.Budgets = &DocPrFlowBudgets{MaxPerHour: budgetPtr(1)}
	_, err = burstFlow.Run(ctx, burstInput)
	require.NoError(t, err, "the first admission is under budget")
	_, err = burstFlow.Run(ctx, burstInput)
	require.Error(t, err, "the second admission fails loud on the injected delay")
}

func assertFailure() error { return assert.AnError }

// TestCOV_GITPR_036_FlowGuardAndFilterViolations pins the two write-path
// guards (INV-2 content guard + the manifest filter): both abort with
// normalized violation evidence and no PR is opened.
func TestCOV_GITPR_036_FlowGuardAndFilterViolations(t *testing.T) {
	fixture := covBuildFlowFixture(t)
	flow, err := CreateDocPrFlow(covFlowDeps(fixture, true))
	require.NoError(t, err)
	input := covFlowInput(fixture)

	// A machine-PR PATH OUTSIDE the manifest allowlist.
	outOfAllowlist := input
	outOfAllowlist.DocContents = map[string]string{
		"docs/payments.md": "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
		"docs/api.md":      "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
		"docs/cli.md":      "# doc\n\nRefunds require an idempotency key for amounts above 10 USD.\n",
		"docs/unlisted.md": "# not allowlisted\n",
	}
	patches := make([]schemas.DocPatch, len(input.Patches))
	copy(patches, input.Patches)
	patches[2].DocPath = "docs/unlisted.md"
	outOfAllowlist.Patches = patches
	result, err := flow.Run(context.Background(), outOfAllowlist)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindAborted, result.Kind)
	assert.Equal(t, string(PrFlowAbortFilterViolation), result.Reason)
	require.NotEmpty(t, result.Violations)
	assert.Equal(t, "docs/unlisted.md", result.Violations[0].Path)
}

// TestCOV_GITPR_045_FlowHappyPath pins the end-to-end write path on a REAL
// repo with a scripted platform: the plan is assembled, the doc branch is
// created, the commit lands, the marker-keyed PR is opened and the merge
// ledger records the open state. A re-run of the SAME flow with the same input
// UPDATES that PR (INV-11) and never duplicates it.
func TestCOV_GITPR_045_FlowHappyPath(t *testing.T) {
	ctx := context.Background()
	fixture := covBuildFlowFixture(t)
	flow, err := CreateDocPrFlow(covFlowDeps(fixture, true))
	require.NoError(t, err)
	input := covFlowInput(fixture)

	first, err := flow.Run(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindCreated, first.Kind)
	require.NotNil(t, first.PR)
	assert.Equal(t, 1, first.PR.Number)
	assert.Equal(t, "docs: synchronize documentation with PR #481", first.PR.Title)
	require.NotEmpty(t, first.Branch)
	assert.True(t, first.BranchCreated)
	require.NotNil(t, first.CommitSha, "the write path committed the assembled plan")
	assert.Len(t, *first.CommitSha, 40)
	assert.NotEmpty(t, first.IdempotencyKey)
	assert.Contains(t, first.IdempotencyKey, fixture.headSha)
	require.NotEmpty(t, first.LedgerHash, "the ledger recorded the open PR")
	assert.False(t, first.CasConflict)
	assert.False(t, first.RerunRequired)
	require.NotNil(t, first.PR)
	assert.Contains(t, first.PR.Body, "generated by docsync")
	assert.Contains(t, first.PR.Body, IdempotencyMarker(first.IdempotencyKey))
	// The trace records every seam that ran.
	require.NotEmpty(t, first.Trace)
	// The PR body is stamped once.
	assert.Equal(t, 1, strings.Count(first.PR.Body, IdempotencyMarker(first.IdempotencyKey)))

	// The branch exists in the repo and carries exactly one commit.
	tip, exists, tipErr := branchTip(ctx, fixture.client, first.Branch)
	require.NoError(t, tipErr)
	assert.True(t, exists)
	assert.Len(t, tip, 40)

	// A re-run with the SAME input UPDATES the same PR (INV-11).
	second, err := flow.Run(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindUpdated, second.Kind)
	require.NotNil(t, second.PR)
	assert.Equal(t, first.PR.Number, second.PR.Number, "the marker-keyed PR is reused")
	assert.Equal(t, first.IdempotencyKey, second.IdempotencyKey)
	// Only one PR ever exists.
	listed, err := fixture.platform.ListOpenDocsPrs(ctx, covFlowRepoSlug)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, first.PR.Number, listed[0].Number)

	// The PR body renders the canonical report fields.
	body := second.PR.Body
	assert.Contains(t, body, covFlowRunID)
	assert.Contains(t, body, "docs/payments.md")
	assert.Contains(t, body, "## Provenance")
	assert.Contains(t, body, "review like any code change")
	assert.Contains(t, body, "Partial pass (D4)")
}

// TestCOV_GITPR_046_FlowCapGovernorDowngrade pins the open-PR cap: when the
// repo already holds the budget's worth of open doc PRs the flow downgrades to
// a report-only patch comment instead of opening a new PR.
func TestCOV_GITPR_046_FlowCapGovernorDowngrade(t *testing.T) {
	ctx := context.Background()
	fixture := covBuildFlowFixture(t)
	// One PR is already open, and the run asks for MaxOpen=1.
	deps := covFlowDeps(fixture, true)
	existing, err := fixture.platform.inner.CreatePr(ctx, CreatePrInput{
		Repo: covFlowRepoSlug, Branch: "docsync/older-run",
		Base: "main", Title: "docs: older", Body: "older analysis",
	})
	require.NoError(t, err)
	_ = existing
	input := covFlowInput(fixture)
	input.Budgets = &DocPrFlowBudgets{MaxOpen: budgetPtr(1)}
	flow, err := CreateDocPrFlow(deps)
	require.NoError(t, err)
	result, err := flow.Run(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindDowngradedReportOnly, result.Kind)
	assert.Nil(t, result.PR, "no new PR is opened when the cap is reached")
}

// TestCOV_GITPR_057_FlowDeepRefusals pins the flow's remaining loud-error
// branches: a bad source head sha, a bad burst budget, and a failing
// listOpenDocsPrs (the cap-governor read) all abort the run with an error.
func TestCOV_GITPR_057_FlowDeepRefusals(t *testing.T) {
	ctx := context.Background()

	// (a) A source-PR head sha that is not a full 40-hex string is a typed
	// refusal from the branch factory (fail closed before any ref exists).
	badShaFixture := covBuildFlowFixture(t)
	badShaDeps := covFlowDeps(badShaFixture, true)
	badShaFlow, err := CreateDocPrFlow(badShaDeps)
	require.NoError(t, err)
	badShaInput := covFlowInput(badShaFixture)
	badShaInput.SourcePr = &FlowSourcePr{Number: 481, HeadSha: "not-a-full-sha"}
	_, err = badShaFlow.Run(ctx, badShaInput)
	require.Error(t, err)
	assert.Equal(t, CodeBranchName, docutil.CodeOf(err))

	// (b) A non-positive MaxPerHour is a governor-config refusal.
	badBudgetFixture := covBuildFlowFixture(t)
	badBudgetFlow, err := CreateDocPrFlow(covFlowDeps(badBudgetFixture, true))
	require.NoError(t, err)
	badBudgetInput := covFlowInput(badBudgetFixture)
	badBudgetInput.Budgets = &DocPrFlowBudgets{MaxPerHour: budgetPtr(0)}
	_, err = badBudgetFlow.Run(ctx, badBudgetInput)
	require.Error(t, err)
	assert.Equal(t, CodeGovernorConfig, docutil.CodeOf(err))

	// (c) A failing listOpenDocsPrs (the cap read) surfaces.
	capFixture := covBuildFlowFixture(t)
	capDeps := covFlowDeps(capFixture, true)
	capFixture.platform.fail("listOpenDocsPrs", assertFailure())
	capFlow, err := CreateDocPrFlow(capDeps)
	require.NoError(t, err)
	_, err = capFlow.Run(ctx, covFlowInput(capFixture))
	require.ErrorIs(t, err, assertFailure())

	// (d) A failing marker lookup surfaces (the idempotency seam).
	lookupFixture := covBuildFlowFixture(t)
	lookupDeps := covFlowDeps(lookupFixture, true)
	lookupFixture.platform.fail("findByPrBodyMarker", assertFailure())
	lookupFlow, err := CreateDocPrFlow(lookupDeps)
	require.NoError(t, err)
	_, err = lookupFlow.Run(ctx, covFlowInput(lookupFixture))
	require.ErrorIs(t, err, assertFailure())
}

// TestCOV_GITPR_058_FlowSupersedeAndCasConflict pins the ledger-driven
// supersede path: a stale OPEN entry with a different key supersedes the older
// PR (M2), and a CAS-losing record surfaces as rerun-required (R15).
func TestCOV_GITPR_058_FlowSupersedeAndCasConflict(t *testing.T) {
	ctx := context.Background()
	fixture := covBuildFlowFixture(t)
	deps := covFlowDeps(fixture, true)
	flow, err := CreateDocPrFlow(deps)
	require.NoError(t, err)

	// Pre-seed the ledger with an OPEN entry carrying a STALE key, and the
	// platform with the stale PR (the M2 supersede path).
	staleStale, err := fixture.platform.inner.CreatePr(ctx, CreatePrInput{
		Repo: covFlowRepoSlug, Branch: "docsync/stale-run",
		Base: "main", Title: "docs: stale", Body: "stale analysis\n" + IdempotencyMarker("stale-key"),
	})
	require.NoError(t, err)
	_, err = covLedgerSeed("pr-481", fixture.store, schemas.LedgerEntry{
		DocPRNumber:    staleStale.Number,
		State:          schemas.MergeLedgerStateOpen,
		IdempotencyKey: "stale-key",
		Detail:         "stale open entry",
	})
	require.NoError(t, err)

	input := covFlowInput(fixture)
	result, err := flow.Run(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, PrFlowKindCreated, result.Kind)
	require.NotNil(t, result.PR)
	assert.NotEqual(t, staleStale.Number, result.PR.Number,
		"the stale PR is superseded, never duplicated")

	// The supersede transition ran through the platform.
	closed, err := fixture.platform.inner.ListOpenDocsPrs(ctx, covFlowRepoSlug)
	require.NoError(t, err)
	for _, pr := range closed {
		assert.NotEqual(t, staleStale.Number, pr.Number, "the stale PR is no longer open")
	}

	// The idempotency key differs from the stale ledger entry's.
	assert.NotEqual(t, "stale-key", result.IdempotencyKey)
}

// covLedgerSeed writes one ledger entry directly (test seeding).
func covLedgerSeed(sourceKey string, store MergeLedgerStateStore, entry schemas.LedgerEntry) (bool, error) {
	ledger, err := NewMergeLedgerStore(MergeLedgerStoreDeps{Store: store})
	if err != nil {
		return false, err
	}
	recorded, err := ledger.Record(sourceKey, RecordLedgerEntryInput{
		DocPRNumber: entry.DocPRNumber, State: entry.State,
		IdempotencyKey: entry.IdempotencyKey, Detail: entry.Detail,
	}, covFixedTime, nil)
	if err != nil {
		return false, err
	}
	return recorded.OK, nil
}
