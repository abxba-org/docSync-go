// Ported from packages/policy/test/shadow-replay.test.ts (P12-T01; V-P12-2; ARCH §8.6, §7.1; D6):
// the offline historical-replay lane — candidate vs production over the
// Phase-10 frozen split (consumed as DATA — policy must not import core,
// OD-1), recorded doubles only, zero network/LLM/clock. Determinism and the
// identical-input guarantee are the binding tests here.
//
// Determinism note (mirrors the TS): the replay has NO randomness and NO
// clock to inject — the draw-free engine is deterministic by construction and
// `now` is injected per call (shadowReplayNow below, mirroring the TS NOW).
package policy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- fixture */

// shadowStrPtr models the TS optional string (undefined ⇒ nil).
func shadowStrPtr(value string) *string { return &value }

func shadowReplayAnalysisFor(caseID string) schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "acme/widgets#" + caseID,
		Repo:                "acme/widgets",
		BaseSHA:             "base-" + caseID,
		HeadSHA:             "head-" + caseID,
		Files:               []schemas.ChangedFile{{Path: "src/" + caseID + ".ts", Status: schemas.FileStatusModified}},
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: shadowStrPtr("fixture replay case")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

func shadowReplayInputsFor(caseID string) policy.ShadowReplayInputs {
	return policy.ShadowReplayInputs{
		ChangeAnalysis: shadowReplayAnalysisFor(caseID),
		// Deliberately unsorted: the canonical input hash must be order-free.
		CandidateSet:   []string{"docs/api.md", "docs/guide.md"},
		DiffSubsetHash: docutil.ContentHash(caseID),
	}
}

// shadowSplit is the deterministic fixture dataset: the 8-case frozen split
// over the replay corpus. In production the split manifest is core's
// buildFrozenSplit output (P10-T08) consumed as data (OD-1) — a split
// manifest is data, not a snapshot copy.
var shadowSplit = policy.ShadowReplaySplit{
	SplitID: "frozen-42-shadow-fixture",
	CaseIDs: []string{"case-01", "case-02", "case-04", "case-05", "case-07", "case-08", "case-10", "case-12"},
}

// Recorded production (v17) vs candidate (v18) decision actions — a mix that
// yields both-ignore, both-review and mixed cells (ARCH §8.6 example).
var shadowProductionActions = map[string]schemas.Action{
	"case-01": schemas.ActionReview,
	"case-02": schemas.ActionIgnore,
	"case-04": schemas.ActionReview,
	"case-05": schemas.ActionIgnore,
	"case-07": schemas.ActionIgnore,
	"case-08": schemas.ActionIgnore,
	"case-10": schemas.ActionIgnore,
	"case-12": schemas.ActionReview,
}

var shadowCandidateActions = map[string]schemas.Action{
	"case-01": schemas.ActionReview,
	"case-02": schemas.ActionIgnore,
	"case-04": schemas.ActionReview,
	"case-05": schemas.ActionReview,
	"case-07": schemas.ActionReview,
	"case-08": schemas.ActionIgnore,
	"case-10": schemas.ActionIgnore,
	"case-12": schemas.ActionIgnore,
}

// shadowRecordedPort is the TS recordedPort: a recorded double whose
// observedInputHash is the canonical case hash unless a tamper override
// (nil = TS `undefined`) replaces it.
func shadowRecordedPort(t *testing.T, outcomes map[string]schemas.Action, tamper func(caseID string) *string) policy.ShadowReplayPort {
	t.Helper()
	return func(caseInput policy.ShadowReplayCase) policy.ShadowReplayPortResult {
		hash, err := policy.ShadowReplayInputHash(caseInput.Inputs)
		require.NoError(t, err)
		observed := hash
		if tamper != nil {
			if override := tamper(caseInput.CaseID); override != nil {
				observed = *override
			}
		}
		return policy.ShadowReplayPortResult{Action: outcomes[caseInput.CaseID], ObservedInputHash: observed}
	}
}

// shadowReplayNow is the injected instant (the TS NOW) — never a clock.
const shadowReplayNow = "2026-10-09T00:00:00.000Z"

func shadowReplayInput(production, candidate policy.ShadowReplayPort) policy.ShadowReplayInput {
	cases := make([]policy.ShadowReplayCase, 0, len(shadowSplit.CaseIDs))
	for _, caseID := range shadowSplit.CaseIDs {
		cases = append(cases, policy.ShadowReplayCase{CaseID: caseID, Inputs: shadowReplayInputsFor(caseID)})
	}
	return policy.ShadowReplayInput{
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Split:                   shadowSplit,
		Cases:                   cases,
		ProductionReplay:        production,
		CandidateReplay:         candidate,
		Now:                     shadowReplayNow,
	}
}

func shadowReplayRunOK(t *testing.T, input policy.ShadowReplayInput) policy.ShadowReplayResult {
	t.Helper()
	result, err := policy.RunShadowReplay(input)
	require.NoError(t, err)
	return result
}

/* ----------------------------------------------------------------- tests */

// TS: 'V-P12-2: the shadow harness asserts candidate and production received
// identical ChangeAnalysis + candidate sets (recorded input hashes equal)'.
func TestShadowReplay_VP12_2_IdenticalInputsRecorded(t *testing.T) {
	result := shadowReplayRunOK(t, shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, nil)))

	// Every case records BOTH policies' observed input hashes, equal to the
	// canonical case hash — the identical-input guarantee, persisted.
	for _, pair := range result.Pairs {
		assert.Equal(t, pair.InputHash, pair.ObservedProductionInputHash)
		assert.Equal(t, pair.InputHash, pair.ObservedCandidateInputHash)
		recorded, ok := result.Matrix.InputHashes[pair.CaseID]
		require.True(t, ok, "case %s must record its input hashes", pair.CaseID)
		assert.Equal(t, pair.InputHash, recorded.Production)
		assert.Equal(t, pair.InputHash, recorded.Candidate)
		assert.True(t, recorded.Equal)
	}
}

// TS: 'the typed mismatch error names the case and the policy on a tampered
// input path'.
func TestShadowReplay_InputMismatchNamesCaseAndPolicy(t *testing.T) {
	tamperProduction := func(caseID string) *string {
		if caseID == "case-05" {
			return shadowStrPtr(strings.Repeat("f", 64))
		}
		return nil
	}
	_, err := policy.RunShadowReplay(shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, tamperProduction),
		shadowRecordedPort(t, shadowCandidateActions, nil)))
	require.Error(t, err)
	var mismatch *policy.ShadowInputMismatchError
	require.ErrorAs(t, err, &mismatch)
	assert.Equal(t, "case-05", mismatch.CaseID)
	assert.Equal(t, "production", mismatch.Policy)
	assert.Equal(t, docutil.CodeValidation, mismatch.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)
	assert.Contains(t, mismatch.Error(), "identical")
	assert.Contains(t, mismatch.Error(), "shadow input mismatch (V-P12-2)")
	assert.Equal(t, strings.Repeat("f", 64), mismatch.Observed)
	assert.NotEmpty(t, mismatch.Expected)

	tamperCandidate := func(caseID string) *string {
		if caseID == "case-07" {
			return shadowStrPtr(strings.Repeat("0", 64))
		}
		return nil
	}
	_, err = policy.RunShadowReplay(shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, tamperCandidate)))
	require.Error(t, err)
	require.ErrorAs(t, err, &mismatch)
	assertShadowErrorCoded(t, err, docutil.CodeValidation)
	assert.Equal(t, "case-07", mismatch.CaseID)
	assert.Equal(t, "candidate", mismatch.Policy)
}

// TS: 'P12-T01: replay is deterministic (two runs byte-identical)'.
func TestShadowReplay_P12_T01_DeterministicByteIdentical(t *testing.T) {
	input := shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, nil))
	first := shadowReplayRunOK(t, input)
	second := shadowReplayRunOK(t, input)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	assert.Equal(t, string(firstJSON), string(secondJSON))

	// The canonical input hash is order-free over the candidate set.
	unsorted, err := policy.ShadowReplayInputHash(shadowReplayInputsFor("case-01"))
	require.NoError(t, err)
	sortedInputs := shadowReplayInputsFor("case-01")
	sortedInputs.CandidateSet = []string{"docs/guide.md", "docs/api.md"}
	sorted, err := policy.ShadowReplayInputHash(sortedInputs)
	require.NoError(t, err)
	assert.Equal(t, unsorted, sorted)
}

// TS: 'fails closed when the split references cases without recorded inputs'.
func TestShadowReplay_CoverageErrorOnMissingRecordedInputs(t *testing.T) {
	split := shadowSplit
	split.CaseIDs = append(append([]string(nil), shadowSplit.CaseIDs...), "case-99")
	input := shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, nil))
	input.Split = split

	_, err := policy.RunShadowReplay(input)
	require.Error(t, err)
	var coverage *policy.ShadowReplayCoverageError
	require.ErrorAs(t, err, &coverage)
	assert.Equal(t, docutil.CodeValidation, coverage.Code())
	assert.Contains(t, coverage.Error(), "case-99")
}

// TS: 'refuses a shadow run comparing a policy against itself'.
func TestShadowReplay_RefusesPolicyVersionsEqual(t *testing.T) {
	input := shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, nil))
	input.CandidatePolicyVersion = 17

	_, err := policy.RunShadowReplay(input)
	require.Error(t, err)
	var inputErr *policy.ShadowReplayInputError
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "must differ")
}

// assertShadowErrorCoded asserts the typed shadow errors carry their coded
// error through Unwrap (errors.As) and expose it through Code().
func assertShadowErrorCoded(t *testing.T, err error, wantCode string) {
	t.Helper()
	type coder interface{ Code() string }
	coded, ok := err.(coder)
	require.True(t, ok, "shadow errors must expose Code()")
	assert.Equal(t, wantCode, coded.Code())
	var inner *docutil.Error
	require.ErrorAs(t, err, &inner)
	assert.Equal(t, wantCode, inner.Code)
}

// Fail-closed edge branches of the replay engine (the TS test file pins the
// happy path and the tamper/coverage failures; these complete the port):
// malformed instants, duplicate recorded cases, off-vocabulary port decisions
// and malformed recorded inputs must all fail closed with the typed errors.
func TestShadowReplay_FailClosedEdgeBranches(t *testing.T) {
	base := shadowReplayInput(
		shadowRecordedPort(t, shadowProductionActions, nil),
		shadowRecordedPort(t, shadowCandidateActions, nil))

	// A non-canonical instant — and an impossible-calendar instant — fail closed.
	for _, now := range []string{"not-an-instant", "2026-13-45T00:00:00Z"} {
		badNow := base
		badNow.Now = now
		_, err := policy.RunShadowReplay(badNow)
		require.Error(t, err, now)
		var inputErr *policy.ShadowReplayInputError
		require.ErrorAs(t, err, &inputErr)
		assert.Contains(t, inputErr.Error(), "must be a canonical ISO-8601 instant")
		assertShadowErrorCoded(t, err, docutil.CodeValidation)
	}

	// A duplicate recorded case fails closed.
	dupCases := base
	dupCases.Cases = append(append([]policy.ShadowReplayCase(nil), base.Cases...), base.Cases[0])
	_, err := policy.RunShadowReplay(dupCases)
	require.Error(t, err)
	var coverage *policy.ShadowReplayCoverageError
	require.ErrorAs(t, err, &coverage)
	assert.Contains(t, coverage.Error(), "duplicate caseId")
	assertShadowErrorCoded(t, err, docutil.CodeValidation)

	// An off-vocabulary port decision fails closed with the MATRIX vocabulary
	// error (one closed decision vocabulary, OD-2) — per side.
	offVocabulary := map[string]schemas.Action{}
	for _, caseID := range shadowSplit.CaseIDs {
		offVocabulary[caseID] = schemas.Action("refactor")
	}
	for _, side := range []string{"productionAction", "candidateAction"} {
		production := shadowRecordedPort(t, shadowProductionActions, nil)
		candidate := shadowRecordedPort(t, shadowCandidateActions, nil)
		if side == "productionAction" {
			production = shadowRecordedPort(t, offVocabulary, nil)
		} else {
			candidate = shadowRecordedPort(t, offVocabulary, nil)
		}
		_, err := policy.RunShadowReplay(shadowReplayInput(production, candidate))
		require.Error(t, err, side)
		var vocabulary *policy.ShadowMatrixVocabularyError
		require.ErrorAs(t, err, &vocabulary)
		assert.Equal(t, side, vocabulary.Field)
		assert.Equal(t, "refactor", vocabulary.Value)
		assertShadowErrorCoded(t, err, docutil.CodeValidation)
	}

	// Malformed recorded inputs fail closed in the canonical input hash.
	badAnalysis := shadowReplayInputsFor("case-01")
	badAnalysis.ChangeAnalysis.Schema = "docsync.change-analysis/v2"
	_, err = policy.ShadowReplayInputHash(badAnalysis)
	require.Error(t, err)
	var inputErr *policy.ShadowReplayInputError
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "not a valid docsync.change-analysis/v1 record")

	emptyCandidate := shadowReplayInputsFor("case-01")
	emptyCandidate.CandidateSet = []string{"docs/api.md", ""}
	_, err = policy.ShadowReplayInputHash(emptyCandidate)
	require.Error(t, err)
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "candidateSet entries must be non-empty strings")

	badDiffHash := shadowReplayInputsFor("case-01")
	badDiffHash.DiffSubsetHash = "not-a-sha256"
	_, err = policy.ShadowReplayInputHash(badDiffHash)
	require.Error(t, err)
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "diffSubsetHash must be a sha256 hex digest")

	// A split case whose recorded inputs are malformed fails closed inside the
	// run — the canonical input hash IS the V-P12-2 boundary.
	badCase := base
	badCase.Cases = append([]policy.ShadowReplayCase(nil), base.Cases...)
	badCase.Cases[0].Inputs.ChangeAnalysis.Schema = "docsync.change-analysis/v2"
	_, err = policy.RunShadowReplay(badCase)
	require.Error(t, err)
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "not a valid docsync.change-analysis/v1 record")
}
