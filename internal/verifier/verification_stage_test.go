// Ported from packages/verifier/test/orchestrator-verification-stage.test.ts
// (V-P5-7 + P5-T07; the port's contract suite).
//
// The suite keeps the TS name→behavior mapping one-to-one: the port's
// RunContext-driven input derivation (policyVersion, runId, the manifest
// allowlist, the §11.3 mode precedence, the graph's contract names), the
// composition-time D13 writer/judge session-collision assert, the typed-error
// propagation posture (the runner degrades a verification failure to review —
// nothing is swallowed into a fabricated report), and the append-only
// persistence seam.
//
// Identifier namespacing: the `vs` (verification-stage) prefix keeps every
// top-level helper disjoint from the concurrent delegates' suites in the
// shared external test package (CONVENTIONS.md).
//
// Replay/recording double discipline (AGENT-behavior.md): the judge double is
// request-identity keyed and dispatch-per-call — never positional FIFO; an
// invocation without the identity marker fails loud.
package verifier_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/graph"
	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/verifier"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- test doubles */

// vsStoreDouble is the record-store double the stage's optional persistence
// seam consumes (append-only recorder; the real store's report_hash dedup is
// its own contract). Keyed by the verification report's identity (doc path +
// patch ref), never positional.
type vsStoreDouble struct {
	mu      sync.Mutex
	saved   []schemas.VerificationReport
	clocks  []string
	failure error
}

func (s *vsStoreDouble) Save(report schemas.VerificationReport, now func() string) (verifier.VerificationSaveOutcome, error) {
	if s.failure != nil {
		return verifier.VerificationSaveOutcome{}, s.failure
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, report)
	clock := ""
	if now != nil {
		clock = now()
	}
	s.clocks = append(s.clocks, clock)
	return verifier.VerificationSaveOutcome{}, nil
}

func (s *vsStoreDouble) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

/* ----------------------------------------------------------------- fixtures */

// vsCtx is the TS testRunCtx stand-in (P5-T07): the RunContext view the
// verifier derives from (repo, refs, run id + run key, the manifest allowlist,
// the graph, the injected clock).
func vsCtx(manifest schemas.Manifest, graphSnapshot graph.DocumentGraph) verifier.RunContext {
	return verifier.RunContext{
		Repo:     "acme/payments",
		RepoRoot: "/repo",
		Base:     "verify-p4~1",
		Head:     "verify-p4",
		RunID:    vpRunID,
		Key: schemas.RunKey{
			Repo:            "acme/payments",
			BaseSHA:         "verify-p4-base-sha",
			HeadSHA:         "verify-p4-head-sha",
			ManifestVersion: 3,
			PolicyVersion:   vpPolicyVer,
		},
		Manifest: manifest,
		Graph:    graphSnapshot,
		Clock:    vpClock,
	}
}

// vsManifest is the fixture manifest: one entry with an explicit mode, no
// defaults.
func vsManifest(mode *schemas.DocMode) schemas.Manifest {
	return schemas.Manifest{
		Documents: []schemas.ManifestEntry{{
			Path:     vpDocPath,
			Sources:  []string{"src/payments/**", "src/api/payments/**"},
			Triggers: []string{"contract-change"},
			Mode:     mode,
		}},
	}
}

// vsDeps is the fixture deps (TS deps: the D13 identities + judge port; every
// optional VerifyPatchInput member stays nil here — the pipeline's other
// inputs come from the run context).
func vsDeps(judge openrouter.JudgeRole) verifier.VerificationStageDeps {
	return verifier.VerificationStageDeps{
		Judge: judge,
		WriterIdentity: verifier.SessionIdentity{
			Role:      verifier.SessionRolePatchWriter,
			Model:     vpWriterModel,
			SessionID: "writer-session-1",
		},
		JudgeIdentity: verifier.SessionIdentity{
			Role:      verifier.SessionRoleJudge,
			Model:     vpRoutedModel,
			SessionID: "judge-session-1",
		},
		// the request-identity marker the recorded judge double keys on (it
		// rides the triggering diff as the judge invocation's untrusted content)
		TriggeringDiff: vpStrPtr(vpTriggerMarker),
	}
}

// vsStageInput is the port input over the fixture patch.
func vsStageInput(ctx verifier.RunContext, patch *schemas.DocPatch, docContent *string) verifier.VerificationStageInput {
	return verifier.VerificationStageInput{Ctx: ctx, Patch: patch, DocContent: docContent}
}

/* ------------------------------------------------------------------- tests */

// P5-T07: the port returns the three-tier report for a valid patch.
func TestVerificationStagePort_ReturnsReportForValidPatch(t *testing.T) {
	judge := vpNewRecordedJudge()
	port, err := verifier.CreateVerificationStagePort(vsDeps(judge))
	require.NoError(t, err)
	require.NotNil(t, port)

	ctx := vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph())
	result, err := port.Verify(vsStageInput(ctx, vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	require.NotNil(t, result.Report)

	report := result.Report
	require.Empty(t, report.ValidateReport())
	assert.Equal(t, vpRunID, report.RunID)
	assert.Equal(t, vpDocPath, report.DocPath)
	assert.Equal(t, vpPolicyVer, report.PolicyVersion)
	assert.Equal(t, schemas.VerdictPass, report.Verdict)
	require.Len(t, report.Tiers, 3)
	assert.Equal(t, 1, judge.invocationCount())
}

// D13 (binding): the writer/judge session-collision assert runs at COMPOSITION
// time — a mis-wired port never exists, and the error carries the stable code.
func TestVerificationStagePort_SessionCollisionFailsAtCompositionTime(t *testing.T) {
	deps := vsDeps(vpNewRecordedJudge())
	deps.JudgeIdentity.SessionID = deps.WriterIdentity.SessionID // the collision

	port, err := verifier.CreateVerificationStagePort(deps)

	require.Error(t, err)
	assert.Nil(t, port)
	var collision *verifier.JudgeWriterSessionCollisionError
	require.True(t, errors.As(err, &collision), "expected *JudgeWriterSessionCollisionError, got %T", err)
	assert.Equal(t, verifier.JudgeWriterSessionCollisionCode, collision.Code())
	assert.Contains(t, collision.Error(), "writer-session-1")
	assert.Contains(t, collision.Error(), "D13")
}

// Deterministic input derivation (the port's own job): the §11.3 mode
// precedence. A doc path ABSENT from the manifest resolves 'external' (D7) —
// the ownership floor then fails closed in Tier 1 while the other checks
// still run.
func TestVerificationStagePort_UnlistedDocResolvesExternalMode(t *testing.T) {
	judge := vpNewRecordedJudge()
	port, err := verifier.CreateVerificationStagePort(vsDeps(judge))
	require.NoError(t, err)

	// the manifest is EMPTY: the doc is unlisted ⇒ 'external', never writable
	empty := schemas.Manifest{}
	ctx := vsCtx(empty, *vpFixtureGraph())
	patch := vpValidCapturePatch(t)
	patch.DocPath = "docs/unlisted.md" // unlisted: not in the (empty) manifest

	result, err := port.Verify(vsStageInput(ctx, patch, vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	require.NotNil(t, result.Report)

	report := result.Report
	assert.Equal(t, schemas.VerdictReject, report.Verdict)

	// the unlisted doc fails the allowlist AND the ownership floor (external)
	allowlist := vpFindCheck(report.Tiers[0], "manifest-allowlist-only")
	require.NotNil(t, allowlist)
	assert.Equal(t, schemas.CheckFail, allowlist.Status)
	ownership := vpFindCheck(report.Tiers[0], "ownership-floor")
	require.NotNil(t, ownership)
	assert.Equal(t, schemas.CheckFail, ownership.Status)
	require.NotNil(t, ownership.Detail)
	assert.Contains(t, *ownership.Detail, "external")
}

// Deterministic input derivation: entry mode → defaults mode → built-in
// 'suggest' (TS docModeOf precedence).
func TestVerificationStagePort_ModePrecedence(t *testing.T) {
	judge := vpNewRecordedJudge()
	port, err := verifier.CreateVerificationStagePort(vsDeps(judge))
	require.NoError(t, err)

	/* entry mode absent + defaults 'locked' ⇒ 'locked' (NOT writable at any
	   confidence — the D2 re-check rejects it) */
	defaults := schemas.ManifestDefaults{Mode: vpModePtr(schemas.DocModeLocked)}
	manifest := vsManifest(nil)
	manifest.Defaults = &defaults
	result, err := port.Verify(vsStageInput(
		vsCtx(manifest, *vpFixtureGraph()), vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	ownership := vpFindCheck(result.Report.Tiers[0], "ownership-floor")
	require.NotNil(t, ownership)
	assert.Equal(t, schemas.CheckFail, ownership.Status)
	require.NotNil(t, ownership.Detail)
	assert.Contains(t, *ownership.Detail, "locked")

	/* entry mode absent + no defaults ⇒ 'suggest' (writable — passes) */
	result, err = port.Verify(vsStageInput(
		vsCtx(vsManifest(nil), *vpFixtureGraph()), vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	ownership = vpFindCheck(result.Report.Tiers[0], "ownership-floor")
	require.NotNil(t, ownership)
	assert.Equal(t, schemas.CheckPass, ownership.Status)

	/* an explicit entry mode wins over the defaults (auto over locked) */
	manifest = vsManifest(vpModePtr(schemas.DocModeAuto))
	manifest.Defaults = &schemas.ManifestDefaults{Mode: vpModePtr(schemas.DocModeLocked)}
	result, err = port.Verify(vsStageInput(
		vsCtx(manifest, *vpFixtureGraph()), vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	ownership = vpFindCheck(result.Report.Tiers[0], "ownership-floor")
	require.NotNil(t, ownership)
	assert.Equal(t, schemas.CheckPass, ownership.Status)
}

// Typed errors propagate (fail loud) — the runner degrades a verification
// failure to review (`verifier_uncertain`); this adapter never swallows it
// into a fabricated report.
func TestVerificationStagePort_TypedErrorsPropagate(t *testing.T) {
	judge := vpNewRecordedJudge()
	port, err := verifier.CreateVerificationStagePort(vsDeps(judge))
	require.NoError(t, err)

	// a schema-invalid patch (no evidence_refs) fails the input gate
	patch := vpValidCapturePatch(t)
	invalid := *patch
	invalid.Hunks = []schemas.PatchHunk{{
		Anchor:     patch.Hunks[0].Anchor,
		DeleteText: "x",
		InsertText: "y",
	}}
	result, err := port.Verify(vsStageInput(
		vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph()),
		&invalid, vpStrPtr(vpFixtureDoc)))

	require.Error(t, err)
	assert.Nil(t, result.Report) // on error, Report is nil
	var notVerifiable *verifier.PatchNotVerifiableError
	require.True(t, errors.As(err, &notVerifiable), "expected *PatchNotVerifiableError, got %T", err)
	assert.Equal(t, verifier.PatchNotVerifiableCode, notVerifiable.Code())
	assert.Equal(t, 0, judge.invocationCount())
}

// The append-only persistence seam: after a successful verify, the store's
// save is called exactly once with the report and the INJECTED clock.
func TestVerificationStagePort_SavesReportAfterSuccessfulVerify(t *testing.T) {
	store := &vsStoreDouble{}
	deps := vsDeps(vpNewRecordedJudge())
	deps.Store = store
	port, err := verifier.CreateVerificationStagePort(deps)
	require.NoError(t, err)

	result, err := port.Verify(vsStageInput(
		vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph()),
		vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)

	assert.Equal(t, 1, store.saveCount())
	require.Len(t, store.saved, 1)
	assert.Equal(t, *result.Report, store.saved[0])
	require.Len(t, store.clocks, 1)
	assert.Equal(t, vpClockValue, store.clocks[0])
}

// A store failure propagates (fail loud) — never a silently unpersisted
// report.
func TestVerificationStagePort_StoreFailurePropagates(t *testing.T) {
	store := &vsStoreDouble{failure: errors.New("CAS conflict — the loser re-runs")}
	deps := vsDeps(vpNewRecordedJudge())
	deps.Store = store
	port, err := verifier.CreateVerificationStagePort(deps)
	require.NoError(t, err)

	_, err = port.Verify(vsStageInput(
		vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph()),
		vpValidCapturePatch(t), vpStrPtr(vpFixtureDoc)))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CAS conflict")
	assert.Equal(t, 0, store.saveCount()) // the failing save records nothing
}

// The port holds NO global state: a repeat verify over the same inputs yields
// a deep-equal report (and the append-only store records both calls).
func TestVerificationStagePort_RepeatVerifyIsDeepEqualAndStateless(t *testing.T) {
	store := &vsStoreDouble{}
	deps := vsDeps(vpNewRecordedJudge())
	deps.Store = store
	port, err := verifier.CreateVerificationStagePort(deps)
	require.NoError(t, err)

	ctx := vsCtx(vsManifest(vpModePtr(schemas.DocModeAuto)), *vpFixtureGraph())
	patch := vpValidCapturePatch(t)
	first, err := port.Verify(vsStageInput(ctx, patch, vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)
	repeat, err := port.Verify(vsStageInput(ctx, patch, vpStrPtr(vpFixtureDoc)))
	require.NoError(t, err)

	assert.Equal(t, first, repeat)
	assert.Equal(t, 2, store.saveCount())
}

func vpModePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }
