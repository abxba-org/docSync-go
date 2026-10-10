// Ported from packages/policy/test/shadow-live.test.ts (P12-T02; V-P12-1; ARCH §8.6, §7.1; D6; OD-R29):
// the sampled live-shadow runner. The contract under test is the ZERO-EFFECT
// guarantee (V-P12-1): shadow decisions are never surfaced to developers —
// the developer-visible output comes back byte-identical whether shadow is on
// or off.
//
// Determinism note (mirrors the TS): there is NO randomness to seed — the
// draw is sha256(run_id ⊕ NUL ⊕ pr_id) (pinned in the fixtures, so tests
// assert exact outcomes), and `now` is injected via the runner config
// (shadowLiveNow, the injected clock). Tests never depend on wall time.
package policy_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* --------------------------------------------------------------- fixture */

func shadowFloatPtr(value float64) *float64 { return &value }

func shadowIntPtr(value int) *int { return &value }

func shadowModePtr(value policy.LearningMode) *policy.LearningMode { return &value }

func shadowReasonPtr(value schemas.AbstainReason) *schemas.AbstainReason { return &value }

// shadowLiveDiffSubsetHash is the fixture diff-subset slice hash captured at
// recording time (ARCH §7.1).
const shadowLiveDiffSubsetHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// shadowLiveNow / shadowLiveExpiresAt: the injected clock and the resulting
// 14-day time-box (the TS NOW / EXPIRES_AT).
const shadowLiveNow = "2026-10-09T00:00:00.000Z"
const shadowLiveExpiresAt = "2026-10-23T00:00:00.000Z"

func shadowLiveAnalysisFor(caseID string) schemas.ChangeAnalysis {
	return schemas.ChangeAnalysis{
		Schema:              "docsync.change-analysis/v1",
		RunID:               "acme/widgets#" + caseID,
		Repo:                "acme/widgets",
		BaseSHA:             "base-" + caseID,
		HeadSHA:             "head-" + caseID,
		Files:               []schemas.ChangedFile{{Path: "src/" + caseID + ".ts", Status: schemas.FileStatusModified}},
		DependenciesChanged: false,
		ClassificationHints: []schemas.ClassificationHint{},
		DeepAnalysis:        schemas.DeepAnalysis{Status: schemas.DeepAnalysisSkipped, Reason: shadowStrPtr("live-shadow fixture")},
		AnalyzerCoverage:    []schemas.AnalyzerCoverage{{Analyzer: schemas.AnalyzerNameGeneric, Status: schemas.AnalyzerStatusOK}},
	}
}

func shadowLiveInputsFor(caseID string) policy.ShadowReplayInputs {
	return policy.ShadowReplayInputs{
		ChangeAnalysis: shadowLiveAnalysisFor(caseID),
		CandidateSet:   []string{"docs/api.md", "docs/guide.md"},
		DiffSubsetHash: shadowLiveDiffSubsetHash,
	}
}

// shadowDeveloperDecision / shadowDeveloperOutput / shadowDevOutputFor model
// the developer-visible output that must pass the runner untouched (V-P12-1).
type shadowDeveloperDecision struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
}

type shadowDeveloperOutput struct {
	PrID       string                  `json:"prId"`
	Decision   shadowDeveloperDecision `json:"decision"`
	Candidates []string                `json:"candidates"`
}

func shadowDevOutputFor(prID string) shadowDeveloperOutput {
	return shadowDeveloperOutput{
		PrID:       prID,
		Decision:   shadowDeveloperDecision{Action: "ignore", Confidence: 0.97},
		Candidates: []string{"docs/api.md"},
	}
}

// shadowIgnoreLivePort is the TS `candidateShadowPort: () => ({ action:
// 'ignore', abstained: false })` recorded double.
func shadowIgnoreLivePort(policy.LiveShadowPortInput) policy.LiveShadowDecision {
	return policy.LiveShadowDecision{Action: schemas.ActionIgnore, Abstained: false}
}

type shadowLiveOverrides struct {
	learningMode *policy.LearningMode
	fraction     *float64
	maxAgeDays   *int
	port         policy.LiveShadowPort
}

// shadowLiveRunner mirrors the TS runner() helper: a canary-eligible runner
// whose default injected port records the PR ids it was called with
// (deterministic double — no network, no OpenRouter).
func shadowLiveRunner(t *testing.T, overrides *shadowLiveOverrides) (*policy.SampledLiveShadowRunner[shadowDeveloperOutput], *[]string) {
	t.Helper()
	mode := policy.LearningModeCanary
	if overrides != nil && overrides.learningMode != nil {
		mode = *overrides.learningMode
	}
	var calls []string
	var port policy.LiveShadowPort
	if overrides != nil && overrides.port != nil {
		port = overrides.port
	} else {
		port = func(input policy.LiveShadowPortInput) policy.LiveShadowDecision {
			calls = append(calls, input.PrID)
			return policy.LiveShadowDecision{Action: schemas.ActionReview, Abstained: false}
		}
	}
	config := policy.LiveShadowRunnerConfig{
		RunID:                   "shadow-v18-canary-01",
		Repo:                    "acme/widgets",
		LearningMode:            mode,
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Now:                     shadowLiveNow,
		CandidateShadowPort:     port,
	}
	if overrides != nil {
		config.Fraction = overrides.fraction
		config.MaxAgeDays = overrides.maxAgeDays
	}
	runner, err := policy.CreateLiveShadowRunner[shadowDeveloperOutput](config)
	require.NoError(t, err)
	return runner, &calls
}

func shadowLivePRIDs10() []string {
	return []string{"pr-00", "pr-01", "pr-02", "pr-03", "pr-04", "pr-05", "pr-06", "pr-07", "pr-08", "pr-09"}
}

func shadowLivePRIDs24() []string {
	ids := make([]string, 0, 24)
	for index := 0; index < 24; index++ {
		ids = append(ids, fmt.Sprintf("pr-%02d", index))
	}
	return ids
}

/* ----------------------------------------------------------------- tests */

// TS: 'V-P12-1: shadow runs never change developer-visible output'.
func TestLiveShadow_VP12_1_DeveloperOutputZeroEffect(t *testing.T) {
	on, onCalls := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(1)})
	off, offCalls := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(0)})

	var onTurns, offTurns []policy.LiveShadowTurn[shadowDeveloperOutput]
	for _, prID := range shadowLivePRIDs10() {
		inputs := shadowLiveInputsFor(prID)
		onTurn, err := on.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
			PrID:            prID,
			DeveloperOutput: shadowDevOutputFor(prID),
			Inputs:          &inputs,
		})
		require.NoError(t, err)
		onTurns = append(onTurns, onTurn)
		offTurn, err := off.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
			PrID:            prID,
			DeveloperOutput: shadowDevOutputFor(prID),
		})
		require.NoError(t, err)
		offTurns = append(offTurns, offTurn)
	}

	// Shadow on and off: the developer-visible output is byte-identical.
	for index := range onTurns {
		onJSON, err := json.Marshal(onTurns[index].DeveloperOutput)
		require.NoError(t, err)
		offJSON, err := json.Marshal(offTurns[index].DeveloperOutput)
		require.NoError(t, err)
		assert.Equal(t, string(offJSON), string(onJSON))
	}

	// Shadow decisions appear ONLY inside the shadow record — never on the
	// developer-visible output.
	for index, turn := range onTurns {
		require.NotNil(t, turn.ShadowRecord, "sampled turn %d must record its shadow decision", index)
		assert.True(t, turn.ShadowRecord.ShadowOnly)
		require.NotNil(t, turn.ShadowRecord.Decision)
		assert.Equal(t, schemas.ActionReview, turn.ShadowRecord.Decision.Action)
		developerJSON, err := json.Marshal(turn.DeveloperOutput)
		require.NoError(t, err)
		var developerKeys map[string]any
		require.NoError(t, json.Unmarshal(developerJSON, &developerKeys))
		assert.Len(t, developerKeys, 3)
		assert.Contains(t, developerKeys, "prId")
		assert.Contains(t, developerKeys, "decision")
		assert.Contains(t, developerKeys, "candidates")
		assert.NotContains(t, string(developerJSON), "shadow")
	}

	// Unsampled turns carry no shadow record at all.
	for index, turn := range offTurns {
		assert.Nil(t, turn.ShadowRecord, "unsampled turn %d must not record", index)
	}

	assert.Equal(t, shadowLivePRIDs10(), *onCalls)
	assert.Empty(t, *offCalls)
}

// TS: 'P12-T02: sampling is deterministic (sha256-keyed, no randomness) and
// monotonic in the fraction'.
func TestLiveShadow_P12_T02_DeterministicSamplingMonotonic(t *testing.T) {
	// Two runners with the same parameters draw identically (the "same
	// parameters ⇒ same draws" contract).
	a, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(0.5)})
	b, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(0.5)})
	first, err := a.ShouldSample("pr-1")
	require.NoError(t, err)
	second, err := b.ShouldSample("pr-1")
	require.NoError(t, err)
	again, err := a.ShouldSample("pr-1")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, first, again)
	assert.NotEmpty(t, first.DrawHex)
	assert.NotEmpty(t, first.ThresholdHex)

	prIDs := shadowLivePRIDs24()
	sampledAt := func(fraction float64) map[string]bool {
		set := make(map[string]bool)
		runner, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(fraction)})
		for _, prID := range prIDs {
			decision, err := runner.ShouldSample(prID)
			require.NoError(t, err)
			if decision.Sampled {
				set[prID] = true
			}
		}
		return set
	}
	at10 := sampledAt(0.1)
	at50 := sampledAt(0.5)
	at100 := sampledAt(1)
	for prID := range at10 {
		assert.True(t, at50[prID])
		assert.True(t, at100[prID])
	}
	for prID := range at50 {
		assert.True(t, at100[prID])
	}
	assert.Len(t, at100, 24)
	assert.Len(t, sampledAt(0), 0)

	// OD-R29: the shipped default is the recorded recommendation (10%), the
	// fixture fraction IS the runner's fraction, and a NON-eligible repo is
	// never sampled even at fraction 1 (the eligibility gate fails first).
	assert.Equal(t, 0.1, policy.DefaultSampledShadowFraction)
	assert.Equal(t, 0.5, a.Fraction)

	ineligible, _ := shadowLiveRunner(t, &shadowLiveOverrides{
		learningMode: shadowModePtr(policy.LearningModePropose),
		fraction:     shadowFloatPtr(1),
	})
	assert.False(t, ineligible.Eligible)
	for _, prID := range prIDs {
		decision, err := ineligible.ShouldSample(prID)
		require.NoError(t, err)
		assert.False(t, decision.Sampled)
		assert.False(t, decision.Eligible)
		assert.Contains(t, decision.Reason, "OD-R29")
	}
	ineligibleTurn, err := ineligible.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: shadowDevOutputFor("pr-1"),
		Inputs:          nil,
	})
	require.NoError(t, err)
	assert.Nil(t, ineligibleTurn.ShadowRecord)
	ineligibleJSON, err := json.Marshal(ineligibleTurn.DeveloperOutput)
	require.NoError(t, err)
	expectedJSON, err := json.Marshal(shadowDevOutputFor("pr-1"))
	require.NoError(t, err)
	assert.Equal(t, string(expectedJSON), string(ineligibleJSON))

	// Sampled records carry shadow_only: true, both policy versions, the
	// injected created_at and the 14-day expires_at — and expiry flips exactly
	// at the time-box boundary (ARCH §8.6; enforcement is P12-T05).
	sampled, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(1)})
	developerOutput := shadowDevOutputFor("pr-1")
	inputs := shadowLiveInputsFor("pr-1")
	recordTurn, err := sampled.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: developerOutput,
		Inputs:          &inputs,
	})
	require.NoError(t, err)
	record := recordTurn.ShadowRecord
	require.NotNil(t, record)
	assert.True(t, record.ShadowOnly)
	require.NotNil(t, record.Decision)
	assert.Equal(t, schemas.ActionReview, record.Decision.Action)
	assert.True(t, record.Sampled)
	assert.True(t, record.Eligible)
	assert.Equal(t, schemas.ShadowRecordedPolicy{ProductionVersion: 17, CandidateVersion: 18}, record.Policy)
	assert.Equal(t, shadowLiveNow, record.CreatedAt)
	assert.Equal(t, shadowLiveExpiresAt, record.ExpiresAt)
	stale, err := policy.IsShadowRecordExpired(*record, "2026-10-22T23:59:59.000Z")
	require.NoError(t, err)
	assert.False(t, stale)
	atBoundary, err := policy.IsShadowRecordExpired(*record, shadowLiveExpiresAt)
	require.NoError(t, err)
	assert.True(t, atBoundary)

	// The developer-visible output is passed through by the SAME reference
	// (TS `expect(recordTurn.developerOutput).toBe(developerOutput)`).
	pointerRunner, err := policy.CreateLiveShadowRunner[*shadowDeveloperOutput](policy.LiveShadowRunnerConfig{
		RunID:                   "shadow-v18-canary-01",
		Repo:                    "acme/widgets",
		LearningMode:            policy.LearningModeCanary,
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Fraction:                shadowFloatPtr(1),
		Now:                     shadowLiveNow,
		CandidateShadowPort:     shadowIgnoreLivePort,
	})
	require.NoError(t, err)
	pointerOutput := shadowDevOutputFor("pr-1")
	pointerOutputPtr := &pointerOutput
	pointerTurn, err := pointerRunner.ConsiderTurn(policy.LiveShadowConsiderInput[*shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: pointerOutputPtr,
		Inputs:          &inputs,
	})
	require.NoError(t, err)
	assert.Same(t, pointerOutputPtr, pointerTurn.DeveloperOutput)
}

// TS: 'run configuration fails closed on an invalid fraction or a
// self-comparison' and 'a sampled turn without inputs or an off-vocabulary
// decision fails closed'.
func TestLiveShadow_ConfigAndInputFailClosed(t *testing.T) {
	// The time-box default is 14 days (time-boxed canaries, ARCH §8.6).
	assert.Equal(t, 14, policy.DefaultShadowMaxAgeDays)

	baseConfig := policy.LiveShadowRunnerConfig{
		RunID:                   "shadow-v18-canary-01",
		Repo:                    "acme/widgets",
		LearningMode:            policy.LearningModeCanary,
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Fraction:                shadowFloatPtr(1),
		Now:                     shadowLiveNow,
		CandidateShadowPort:     shadowIgnoreLivePort,
	}

	// An out-of-range fraction fails closed before any PR is considered.
	badFraction := baseConfig
	badFraction.Fraction = shadowFloatPtr(1.5)
	_, err := policy.CreateLiveShadowRunner[shadowDeveloperOutput](badFraction)
	require.Error(t, err)
	var configErr *policy.LiveShadowConfigError
	require.ErrorAs(t, err, &configErr)
	assert.Contains(t, configErr.Error(), "fraction must be a number in [0, 1] (got 1.5)")

	// A self-comparison fails closed.
	selfComparison := baseConfig
	selfComparison.CandidatePolicyVersion = 17
	_, err = policy.CreateLiveShadowRunner[shadowDeveloperOutput](selfComparison)
	require.Error(t, err)
	require.ErrorAs(t, err, &configErr)
	assert.Contains(t, configErr.Error(), "must differ")

	// An unknown learning mode fails closed.
	badMode := baseConfig
	badMode.LearningMode = policy.LearningMode("sideways")
	_, err = policy.CreateLiveShadowRunner[shadowDeveloperOutput](badMode)
	require.Error(t, err)
	require.ErrorAs(t, err, &configErr)
	assert.Contains(t, configErr.Error(), "learningMode")

	// A non-canonical instant fails closed.
	badNow := baseConfig
	badNow.Now = "2026-10-09T00:00:00Z"
	_, err = policy.CreateLiveShadowRunner[shadowDeveloperOutput](badNow)
	require.Error(t, err)
	require.ErrorAs(t, err, &configErr)
	assert.Contains(t, configErr.Error(), "now must be a canonical ISO-8601 instant")

	// A sampled turn without inputs fails closed (V-P12-2 lineage required).
	sampled, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(1)})
	_, err = sampled.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: shadowDevOutputFor("pr-1"),
	})
	require.Error(t, err)
	var inputErr *policy.ShadowLiveInputError
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "no shadow inputs were provided")

	// An off-vocabulary shadow decision fails closed.
	offVocabulary, _ := shadowLiveRunner(t, &shadowLiveOverrides{
		fraction: shadowFloatPtr(1),
		port: func(policy.LiveShadowPortInput) policy.LiveShadowDecision {
			return policy.LiveShadowDecision{Action: schemas.Action("refactor"), Abstained: false}
		},
	})
	inputs := shadowLiveInputsFor("pr-1")
	_, err = offVocabulary.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: shadowDevOutputFor("pr-1"),
		Inputs:          &inputs,
	})
	require.Error(t, err)
	var portErr *policy.ShadowLivePortError
	require.ErrorAs(t, err, &portErr)
	assert.Equal(t, docutil.CodeValidation, portErr.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)
	assert.Contains(t, portErr.Error(), "outside the closed docsync.decision/v1 action vocabulary")
}

// Fail-closed edge branches of the runner (the TS test file pins the fraction,
// self-comparison, missing-inputs and off-vocabulary failures; these complete
// the port): every configuration defect, the malformed-input lineage check,
// the empty-PR-id guard, the record-schema self-check (off-vocabulary abstain
// reason) and the expiry probe fail closed with the typed errors.
func TestLiveShadow_FailClosedEdgeBranches(t *testing.T) {
	baseConfig := policy.LiveShadowRunnerConfig{
		RunID:                   "shadow-v18-canary-01",
		Repo:                    "acme/widgets",
		LearningMode:            policy.LearningModeCanary,
		ProductionPolicyVersion: 17,
		CandidatePolicyVersion:  18,
		Now:                     shadowLiveNow,
		CandidateShadowPort:     shadowIgnoreLivePort,
	}
	for _, testCase := range []struct {
		name     string
		mutate   func(*policy.LiveShadowRunnerConfig)
		contains string
	}{
		{"empty runId", func(config *policy.LiveShadowRunnerConfig) { config.RunID = "" }, "runId must be a non-empty string"},
		{"empty repo", func(config *policy.LiveShadowRunnerConfig) { config.Repo = "" }, "repo must be a non-empty string"},
		{"zero production version", func(config *policy.LiveShadowRunnerConfig) { config.ProductionPolicyVersion = 0 }, "productionPolicyVersion must be a positive integer"},
		{"zero candidate version", func(config *policy.LiveShadowRunnerConfig) { config.CandidatePolicyVersion = 0 }, "candidatePolicyVersion must be a positive integer"},
		{"zero maxAgeDays", func(config *policy.LiveShadowRunnerConfig) { config.MaxAgeDays = shadowIntPtr(0) }, "maxAgeDays must be a positive integer (got 0)"},
		{"non-canonical now", func(config *policy.LiveShadowRunnerConfig) { config.Now = "yesterday" }, "now must be a canonical ISO-8601 instant"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := baseConfig
			testCase.mutate(&config)
			_, err := policy.CreateLiveShadowRunner[shadowDeveloperOutput](config)
			require.Error(t, err)
			var configErr *policy.LiveShadowConfigError
			require.ErrorAs(t, err, &configErr)
			assert.Equal(t, docutil.CodeConfig, configErr.Code())
			assertShadowErrorCoded(t, err, docutil.CodeConfig)
			assert.Contains(t, configErr.Error(), testCase.contains)
		})
	}

	sampled, _ := shadowLiveRunner(t, &shadowLiveOverrides{fraction: shadowFloatPtr(1)})

	// The empty-PR-id guard fails closed, on both entry points.
	_, err := sampled.ShouldSample("")
	require.Error(t, err)
	var inputErr *policy.ShadowLiveInputError
	require.ErrorAs(t, err, &inputErr)
	assert.Equal(t, docutil.CodeValidation, inputErr.Code())
	assertShadowErrorCoded(t, err, docutil.CodeValidation)
	assert.Contains(t, inputErr.Error(), "prId must be a non-empty string")

	_, err = sampled.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{})
	require.Error(t, err)
	require.ErrorAs(t, err, &inputErr)
	assert.Contains(t, inputErr.Error(), "considerTurn: prId must be a non-empty string")

	// A sampled turn whose inputs are malformed fails the V-P12-2 lineage
	// check (canonical ChangeAnalysis) before any record is written.
	badInputs := shadowLiveInputsFor("pr-1")
	badInputs.ChangeAnalysis.RunID = ""
	_, err = sampled.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: shadowDevOutputFor("pr-1"),
		Inputs:          &badInputs,
	})
	require.Error(t, err)
	var replayErr *policy.ShadowReplayInputError
	require.ErrorAs(t, err, &replayErr)
	assert.Contains(t, replayErr.Error(), "not a valid docsync.change-analysis/v1 record")

	// The record-schema self-check fails closed on an off-vocabulary abstain
	// reason (the port is contract-bound by Action + AbstainReason).
	badReason, _ := shadowLiveRunner(t, &shadowLiveOverrides{
		fraction: shadowFloatPtr(1),
		port: func(policy.LiveShadowPortInput) policy.LiveShadowDecision {
			return policy.LiveShadowDecision{
				Action:        schemas.ActionReview,
				Abstained:     true,
				AbstainReason: shadowReasonPtr(schemas.AbstainReason("made_up")),
			}
		},
	})
	inputs := shadowLiveInputsFor("pr-1")
	_, err = badReason.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-1",
		DeveloperOutput: shadowDevOutputFor("pr-1"),
		Inputs:          &inputs,
	})
	require.Error(t, err)
	var recordErr *policy.ShadowLiveRecordError
	require.ErrorAs(t, err, &recordErr)
	assert.Equal(t, docutil.CodeSchema, recordErr.Code())
	assertShadowErrorCoded(t, err, docutil.CodeSchema)
	assert.Contains(t, recordErr.Error(), "failed docsync.shadow-run-record/v1")

	// The expiry probe fails closed on a non-parseable `now`.
	recordTurn, err := sampled.ConsiderTurn(policy.LiveShadowConsiderInput[shadowDeveloperOutput]{
		PrID:            "pr-2",
		DeveloperOutput: shadowDevOutputFor("pr-2"),
		Inputs:          &inputs,
	})
	require.NoError(t, err)
	require.NotNil(t, recordTurn.ShadowRecord)
	_, err = policy.IsShadowRecordExpired(*recordTurn.ShadowRecord, "yesterday")
	require.Error(t, err)
	var expiryErr *policy.ShadowLiveInputError
	require.ErrorAs(t, err, &expiryErr)
	assert.Contains(t, expiryErr.Error(), "now is not a parseable instant")
}
