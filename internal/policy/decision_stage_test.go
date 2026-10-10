package policy_test

// Ported from packages/policy/test/orchestrator-decision-stage.test.ts,
// orchestrator-decision-stage-memo.test.ts and
// orchestrator-decision-bridge.test.ts — the Jev decision-stage suites
// (P5-T03, P5-T10 memoization, review M1 bridge). No network: every port is
// an in-memory scripted double; time is an injected clock (no `new Date()`).
//
// Fidelity notes:
//   - The TS suites build decisions through the real emitDecision gate and
//     compiled workflows through compileBootstrapPolicyV1/compilePolicy; this
//     file does the same through the landed policy.EmitDecision /
//     policy.CompileBootstrapPolicyV1 / policy.CompilePolicy seams.
//   - The TS type-level probes (conditional-type pins) port as reflect-based
//     structural pins plus a runtime bridged-adapter test — see
//     TestV_P5_4_StructuralCompatibilityWithCoreDecisionStagePort.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/* -------------------------------------------------------------- fixtures */

// decisionStageChange is the TS CHANGE fixture (both suites).
var decisionStageChange = policy.JevChangeRefs{
	Commit:            "81af92e",
	DiffRef:           "git:81af92e",
	ChangeAnalysisRef: "analysis/01J9ZK6X8A",
}

// stageTelemetryFixture carries BOTH routed halves (D3/DX-P2-2 pin inputs).
func stageTelemetryFixture() policy.JevTelemetry {
	return policy.JevTelemetry{
		Role:           "decision-engine",
		RequestedModel: "binding/primary",
		RoutedModel:    "provider/routed-9",
		Provider:       stageStrPtr("z-ai"),
		LatencyMS:      120,
		Attempt:        1,
		FallbackUsed:   false,
	}
}

// stageUniversalAnswers is the TS UNIVERSAL_ANSWERS fixture (value +
// probability, all four universal keys).
func stageUniversalAnswers() policy.JevAnswers {
	return policy.JevAnswers{
		"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.87},
		"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.12},
		"affected_doc_count":      {Value: json.RawMessage("2"), Probability: 0.91},
		"documentation_impact":    {Value: json.RawMessage(`"possible"`), Probability: 0.91},
	}
}

// stageManifestDocPaths is the TS MANIFEST_PATHS fixture.
var stageManifestDocPaths = []string{"docs/payments.md", "docs/api.md"}

func stageAnalysisContext(impact schemas.DocumentationImpact) policy.JevAnalysisContext {
	return policy.JevAnalysisContext{
		ChangeKind:          schemas.ChangeKindBehavioral,
		DocumentationImpact: impact,
		Risk:                schemas.RiskLevelMedium,
		Confidence:          0.9,
		Answers:             stageUniversalAnswers(),
		Candidates:          []string{"docs/payments.md"},
		Evidence: []schemas.Evidence{
			{
				Kind: schemas.EvidenceKindSymbolDiff,
				Ref:  "src/payments/refund.ts:42",
				Note: "idempotencyKey became optional",
			},
		},
	}
}

func stageInputFixture(impact schemas.DocumentationImpact) policy.JevDecisionStageInput {
	return policy.JevDecisionStageInput{
		Change:          decisionStageChange,
		AnalysisContext: stageAnalysisContext(impact),
	}
}

// stageScriptedDecision emits the scripted port decision the TS scriptedPort
// helper builds: the context-derived body with action `review` (the runtime's
// routing verdict owns the final action — models propose) and answers keyed
// to the impact.
func stageScriptedDecision(
	compiled *policy.CompiledPolicyWorkflow,
	input policy.JevPortInput,
	impact schemas.DocumentationImpact,
) (*schemas.Decision, error) {
	context := input.AnalysisContext
	answers := stageUniversalAnswers()
	answers["documentation_impact"] = schemas.Answer{
		Value:       json.RawMessage(`"` + string(impact) + `"`),
		Probability: 0.91,
	}
	return policy.EmitDecision(policy.EmitDecisionInput{
		Compiled: compiled,
		Change: schemas.DecisionChange{
			Commit:            input.Change.Commit,
			DiffRef:           input.Change.DiffRef,
			ChangeAnalysisRef: input.Change.ChangeAnalysisRef,
		},
		Answers: answers,
		Body: policy.EmitDecisionBody{
			ChangeKind:          context.ChangeKind,
			DocumentationImpact: impact,
			Risk:                context.Risk,
			Confidence:          0.9,
			Action:              schemas.ActionReview,
			Abstain:             false,
			AbstainReason:       nil,
		},
		Candidates:       context.Candidates,
		ManifestDocPaths: stageManifestDocPaths,
		Evidence:         context.Evidence,
		RoutedModel:      "provider/routed-9",
		LatencyMs:        120,
	})
}

// stageScriptedPort is the in-memory DecisionEnginePort double (the TS
// scriptedPort): it records every request (the fake is keyed by request
// identity — each Decide call captures the exact JevPortInput) and either
// returns a scripted result, rejects, or derives a decision from the request.
type stageScriptedPort struct {
	compiled   *policy.CompiledPolicyWorkflow
	result     *policy.JevPortResult
	rejectWith error
	maxCalls   int // 0 = unlimited; >0 → an error beyond the cap (never runs on a memo hit)

	mu    sync.Mutex
	calls []policy.JevPortInput
}

var _ policy.DecisionEnginePort = (*stageScriptedPort)(nil)

func (p *stageScriptedPort) Decide(input policy.JevPortInput) (policy.JevPortResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, input)
	callCount := len(p.calls)
	p.mu.Unlock()
	if p.maxCalls > 0 && callCount > p.maxCalls {
		return policy.JevPortResult{}, errors.New("port must not run on a memo hit (test bug)")
	}
	if p.rejectWith != nil {
		return policy.JevPortResult{}, p.rejectWith
	}
	if p.result != nil {
		return *p.result, nil
	}
	decision, err := stageScriptedDecision(p.compiled, input, input.AnalysisContext.DocumentationImpact)
	if err != nil {
		return policy.JevPortResult{}, err
	}
	return policy.JevPortResult{
		Kind:      policy.JevPortResultKindDecision,
		Decision:  decision,
		Telemetry: stageTelemetryFixture(),
	}, nil
}

func (p *stageScriptedPort) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// stageBootstrapCompiled compiles the bootstrap policy v1 (TS
// compileBootstrapPolicyV1).
func stageBootstrapCompiled(t *testing.T) *policy.CompiledPolicyWorkflow {
	t.Helper()
	compiled, err := policy.CompileBootstrapPolicyV1()
	require.NoError(t, err)
	require.NotNil(t, compiled)
	return compiled
}

// stagePolicyWire renders a parsed policy into its untrusted wire form (the
// compiler seam) — the same marshal/unmarshal rendering bootstrapPolicyV1Wire
// uses internally.
func stagePolicyWire(t *testing.T, parsed policy.Policy) map[string]any {
	t.Helper()
	raw, err := json.Marshal(parsed)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(raw, &wire))
	return wire
}

// stageCompiledWithVersion compiles the bootstrap policy with an overridden
// policy_version (the TS POLICY_V2 fixture: same questions, different
// version — the memo must MISS).
func stageCompiledWithVersion(t *testing.T, version int) *policy.CompiledPolicyWorkflow {
	t.Helper()
	wire := stagePolicyWire(t, policy.BootstrapPolicyV1)
	wire["policy_version"] = version
	result := policy.CompilePolicy(wire)
	require.True(t, result.OK, "fixture drift: the v%d workflow must compile: %+v", version, result.Diagnostics)
	require.NotNil(t, result.Compiled)
	return result.Compiled
}

// stageDefectiveCompiled is the TS defective fixture: POLICY_EXAMPLE with
// universal false and `api_contract_changed` deliberately absent — the exact
// fixture the universal-coverage gate rejects BEFORE any model call.
func stageDefectiveCompiled(t *testing.T) *policy.CompiledPolicyWorkflow {
	t.Helper()
	questions := []struct {
		Key     string `json:"key"`
		Type    string `json:"type"`
		Prompt  string `json:"prompt"`
		Scoring *struct {
			Min int `json:"min"`
			Max int `json:"max"`
		} `json:"scoring,omitempty"`
		Choices []string `json:"choices,omitempty"`
	}{
		{
			Key:    "public_behavior_changed",
			Type:   "boolean",
			Prompt: "Did the change alter runtime behavior visible to users of this repository?",
		},
		{
			Key:    "affected_doc_count",
			Type:   "score",
			Prompt: "How many known documentation candidates does this change affect?",
			Scoring: &struct {
				Min int `json:"min"`
				Max int `json:"max"`
			}{Min: 0, Max: 50},
		},
		{
			Key:     "documentation_impact",
			Type:    "choice",
			Prompt:  "What is the documentation impact of this change?",
			Choices: []string{"none", "possible", "definite", "unknown"},
		},
	}
	rawQuestions, err := json.Marshal(questions)
	require.NoError(t, err)
	var wireQuestions []any
	require.NoError(t, json.Unmarshal(rawQuestions, &wireQuestions))

	wire := stagePolicyWire(t, policy.PolicyExample)
	wire["policy_version"] = 99
	wire["universal"] = false
	wire["questions"] = wireQuestions
	result := policy.CompilePolicy(wire)
	require.True(t, result.OK, "fixture drift: the defective fixture must compile: %+v", result.Diagnostics)
	require.NotNil(t, result.Compiled)
	return result.Compiled
}

func stageStrPtr(value string) *string { return &value }
func stageBoolPtr(value bool) *bool    { return &value }
func stageIntPtr(value int) *int       { return &value }
func stageFloatPtr(value float64) *float64 {
	return &value
}

// stageRoutedTelemetry is the stage telemetry the port telemetry maps onto:
// the ROUTED halves verbatim (D3/DX-P2-2).
func stageRoutedTelemetry() *policy.JevDecisionStageTelemetry {
	return &policy.JevDecisionStageTelemetry{
		LatencyMS: 120,
		Model:     stageStrPtr("provider/routed-9"),
		Provider:  stageStrPtr("z-ai"),
	}
}

/* ------------------------------- suite 1: outcome mapping (P5-T03) */

func TestJevDecisionStageOutcomeMapping(t *testing.T) {
	compiled := stageBootstrapCompiled(t)

	t.Run("decision-kind-passthrough-carries-decision-and-routed-telemetry", func(t *testing.T) {
		port := &stageScriptedPort{compiled: compiled}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
		})
		result, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)

		assert.False(t, result.Abstained)
		assert.Nil(t, result.AbstainReason)
		// Decision passthrough: the schema-valid Stage-1 outcome — routing
		// verdict possible → review (JevRoutingTable.possible_review).
		require.NotNil(t, result.Decision)
		assert.Equal(t, schemas.DecisionSchemaID, result.Decision.Schema)
		assert.Equal(t, schemas.ActionReview, result.Decision.Action)
		assert.False(t, result.Decision.Abstain)
		require.NotNil(t, result.Decision.Trace)
		assert.Equal(t, "provider/routed-9", result.Decision.Trace.Model)
		// D3/DX-P2-2: telemetry carries the ACTUALLY ROUTED halves from
		// JevTelemetry — latencyMs = latency_ms, model = routed_model — never a
		// configured default.
		assert.Equal(t, stageRoutedTelemetry(), result.Telemetry)
		assert.Equal(t, 1, port.callCount())
	})

	t.Run("abstain-kind-maps-to-abstained-result-with-canonical-reason", func(t *testing.T) {
		port := &stageScriptedPort{
			compiled: compiled,
			result: &policy.JevPortResult{
				Kind:      policy.JevPortResultKindAbstain,
				Reason:    schemas.AbstainReasonSchemaViolation,
				Telemetry: stageTelemetryFixture(),
			},
		}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
		})
		result, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)

		assert.Nil(t, result.Decision) // NEVER a fabricated decision
		assert.True(t, result.Abstained)
		require.NotNil(t, result.AbstainReason)
		assert.Equal(t, schemas.AbstainReasonSchemaViolation, *result.AbstainReason) // canonical reason passthrough
		assert.Equal(t, stageRoutedTelemetry(), result.Telemetry)
		assert.Equal(t, 1, port.callCount())
	})

	t.Run("port-throw-maps-to-operational-abstain", func(t *testing.T) {
		ticks := []int{500, 812}
		read := 0
		clock := func() int {
			if read >= len(ticks) {
				t.Fatal("clock exhausted (test bug)")
			}
			value := ticks[read]
			read++
			return value
		}
		port := &stageScriptedPort{
			compiled:   compiled,
			rejectWith: errors.New("transport down"),
		}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
			Clock:    clock,
		})
		result, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)

		// Operational degradation at the stage boundary — NOT a crash and NOT a
		// fabricated decision: canonical operational reason model_unavailable.
		assert.Nil(t, result.Decision)
		assert.True(t, result.Abstained)
		require.NotNil(t, result.AbstainReason)
		assert.Equal(t, schemas.AbstainReasonModelUnavailable, *result.AbstainReason)
		// Degraded-path stage wall-time attributed from the INJECTED clock
		// (500 → 812); nothing was routed, so no model/provider half exists.
		assert.Equal(t, &policy.JevDecisionStageTelemetry{LatencyMS: 312}, result.Telemetry)
	})

	t.Run("jev-config-error-propagates-never-masked", func(t *testing.T) {
		// A compiled workflow whose composed set misses a universal key — the
		// exact fixture the Jev runtime gate rejects BEFORE any model call.
		defective := stageDefectiveCompiled(t)
		port := &stageScriptedPort{compiled: defective}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *defective,
		})

		_, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		// The JevConfigError SURFACES — the stage does NOT convert the config
		// defect into an abstained result (which would be exit-1 review noise).
		require.Error(t, err)
		var configErr *policy.JevConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, []string{"api_contract_changed"}, configErr.MissingUniversalKeys)
		// Fail closed BEFORE any model call: the port never ran.
		assert.Equal(t, 0, port.callCount())
	})

	t.Run("usage-attribution-passthrough-when-telemetry-carries-usage", func(t *testing.T) {
		// P5-T17 (ARCH §10.6): the port's token/cost usage block threads onto
		// the stage telemetry VERBATIM on the decision path — no normalization,
		// no re-derivation — so the orchestrator can attribute it per
		// policy_version downstream. The routed model/provider halves still pin.
		usage := policy.JevUsage{
			PromptTokens:     1200,
			CompletionTokens: 340,
			TotalTokens:      stageIntPtr(1540),
			Cost:             stageFloatPtr(0.0021),
		}
		context := stageAnalysisContext(schemas.DocumentationImpactPossible)
		decision, err := policy.EmitDecision(policy.EmitDecisionInput{
			Compiled: compiled,
			Change: schemas.DecisionChange{
				Commit:            decisionStageChange.Commit,
				DiffRef:           decisionStageChange.DiffRef,
				ChangeAnalysisRef: decisionStageChange.ChangeAnalysisRef,
			},
			Answers: stageUniversalAnswers(),
			Body: policy.EmitDecisionBody{
				ChangeKind:          context.ChangeKind,
				DocumentationImpact: context.DocumentationImpact,
				Risk:                context.Risk,
				Confidence:          0.9,
				Action:              schemas.ActionReview,
				Abstain:             false,
				AbstainReason:       nil,
			},
			Candidates:       context.Candidates,
			ManifestDocPaths: stageManifestDocPaths,
			Evidence:         context.Evidence,
			RoutedModel:      "provider/routed-9",
			LatencyMs:        120,
		})
		require.NoError(t, err)
		telemetry := stageTelemetryFixture()
		telemetry.Usage = &usage
		port := &stageScriptedPort{
			compiled: compiled,
			result: &policy.JevPortResult{
				Kind:      policy.JevPortResultKindDecision,
				Decision:  decision,
				Telemetry: telemetry,
			},
		}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
		})
		result, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)

		assert.False(t, result.Abstained)
		require.NotNil(t, result.Decision)
		assert.Equal(t, schemas.ActionReview, result.Decision.Action)
		// VERBATIM usage passthrough (prompt/completion + optional total/cost).
		require.NotNil(t, result.Telemetry)
		assert.Equal(t, &usage, result.Telemetry.Usage)
		require.NotNil(t, result.Telemetry.Model)
		assert.Equal(t, "provider/routed-9", *result.Telemetry.Model)
		require.NotNil(t, result.Telemetry.Provider)
		assert.Equal(t, "z-ai", *result.Telemetry.Provider)
		assert.Equal(t, 120, result.Telemetry.LatencyMS)
		assert.Equal(t, 1, port.callCount())
	})
}

/* --------------------- suite 2: memoization (P5-T10; D6 isolation) */

// stageFailingMemo is the TS memoFailure double: a memo that derives the key
// but fails on get and set — a memo failure must never break the stage.
type stageFailingMemo struct{}

var _ policy.JevMemoCachePick = stageFailingMemo{}

func (stageFailingMemo) KeyFor(input policy.JevMemoKeyInput) (string, error) {
	return "k-fixed", nil
}

func (stageFailingMemo) Get(namespace, key string) (*schemas.Decision, error) {
	return nil, errors.New("memo get down")
}

func (stageFailingMemo) Set(namespace, key string, decision *schemas.Decision) error {
	return errors.New("memo set down")
}

func TestJevDecisionStageMemoization(t *testing.T) {
	compiled := stageBootstrapCompiled(t)

	// PORT_STAGE_TELEMETRY: what the memo suite's port telemetry maps onto.
	portStageTelemetry := stageRoutedTelemetry()

	t.Run("V-P5-9 memo-hit-returns-identical-result-without-port-calls", func(t *testing.T) {
		// An explicit maxCalls=1 port: a real memo hit must never reach a
		// second call (this removes any reliance on the mock's internal shape).
		port := &stageScriptedPort{compiled: compiled, maxCalls: 1}
		memo := policy.NewJevMemoCache()
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
			Memo:     memo,
		})

		first, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		assert.Equal(t, 1, port.callCount())
		assert.False(t, first.CacheHit)
		assert.False(t, first.Abstained)
		require.NotNil(t, first.Decision)
		// The port telemetry routed halves map onto the stage telemetry unchanged.
		assert.Equal(t, portStageTelemetry, first.Telemetry)
		// Only the schema-valid Decision is stored.
		assert.Equal(t, policy.JevMemoStats{
			Entries:     1,
			ByNamespace: map[string]int{policy.ProductionNamespace: 1},
		}, memo.Stats())

		second, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		// HIT: the port never ran (any second call returns the port error).
		assert.Equal(t, 1, port.callCount())
		// The cached result returns verbatim: the original decision identity
		// and the additive cache-hit marker.
		assert.True(t, second.CacheHit)
		assert.False(t, second.Abstained)
		assert.Same(t, first.Decision, second.Decision)
		assert.Equal(t, first.Telemetry, second.Telemetry)
		// The cache-hit result equals the fresh result plus the marker.
		withHit := first
		withHit.CacheHit = true
		assert.Equal(t, withHit, second)
		// The store is unchanged by the hit.
		assert.Equal(t, policy.JevMemoStats{
			Entries:     1,
			ByNamespace: map[string]int{policy.ProductionNamespace: 1},
		}, memo.Stats())
	})

	t.Run("V-P5-9 different-policy-version-never-shares-entries", func(t *testing.T) {
		// Each port calls its own compiled workflow — a wrong policy_version
		// key cannot return the other version's decision.
		compiledV1 := compiled
		compiledV2 := stageCompiledWithVersion(t, 2)
		require.Equal(t, 1, compiledV1.Policy.PolicyVersion)
		require.Equal(t, 2, compiledV2.Policy.PolicyVersion)
		portV1 := &stageScriptedPort{compiled: compiledV1}
		portV2 := &stageScriptedPort{compiled: compiledV2}
		memo := policy.NewJevMemoCache()
		stageV1 := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     portV1,
			Workflow: *compiledV1,
			Memo:     memo,
		})
		stageV2 := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     portV2,
			Workflow: *compiledV2,
			Memo:     memo,
		})

		r1, err := stageV1.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, r1.Decision)
		assert.Equal(t, 1, r1.Decision.PolicyVersion)
		assert.False(t, r1.CacheHit)
		assert.Equal(t, 1, portV1.callCount())
		assert.Equal(t, 0, portV2.callCount())

		r2, err := stageV2.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, r2.Decision)
		assert.Equal(t, 2, r2.Decision.PolicyVersion)
		assert.False(t, r2.CacheHit)
		assert.Equal(t, 1, portV2.callCount())
		assert.Equal(t, 1, portV1.callCount())

		// Re-decide on the same diff: each version hits its OWN entry — never
		// the other version's decision.
		r1again, err := stageV1.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, r1again.Decision)
		assert.Equal(t, 1, r1again.Decision.PolicyVersion)
		assert.True(t, r1again.CacheHit)
		assert.Equal(t, 1, portV1.callCount())

		r2again, err := stageV2.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, r2again.Decision)
		assert.Equal(t, 2, r2again.Decision.PolicyVersion)
		assert.True(t, r2again.CacheHit)
		assert.Equal(t, 1, portV2.callCount())

		assert.Equal(t, policy.JevMemoStats{
			Entries:     2,
			ByNamespace: map[string]int{policy.ProductionNamespace: 2},
		}, memo.Stats())
	})

	t.Run("V-P5-9 shadow-keyed-memo-never-leaks-into-production", func(t *testing.T) {
		// One cache, one binding (so memoNamespace is the ONLY isolation
		// layer); the impact flows into BOTH the deterministic analysis context
		// and the scripted decision's answers so the conflicting-signals guard
		// never fires.
		shadowBinding := "shadow:candidate-binding"
		memo := policy.NewJevMemoCache()
		portShadow := &stageScriptedPort{compiled: compiled}
		stageShadow := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:          portShadow,
			Workflow:      *compiled,
			Memo:          memo,
			MemoNamespace: shadowBinding,
		})
		portProduction := &stageScriptedPort{compiled: compiled}
		stageProduction := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     portProduction,
			Workflow: *compiled,
			Memo:     memo,
		})

		// (1) Shadow decision under the shadow namespace — definite impact
		// routes investigate (definite_investigate).
		shadowResult, err := stageShadow.Decide(stageInputFixture(schemas.DocumentationImpactDefinite))
		require.NoError(t, err)
		require.NotNil(t, shadowResult.Decision)
		assert.Equal(t, 1, portShadow.callCount())
		assert.False(t, shadowResult.CacheHit)
		assert.Equal(t, schemas.ActionInvestigate, shadowResult.Decision.Action)
		assert.Equal(t, schemas.DocumentationImpactDefinite, shadowResult.Decision.DocumentationImpact)

		// (2) Production decision — possible impact routes review (the policy
		// of the shadow entry would be investigate — the shadow entry is NOT
		// visible here).
		productionResult, err := stageProduction.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, productionResult.Decision)
		assert.Equal(t, 1, portProduction.callCount())
		assert.False(t, productionResult.CacheHit)
		assert.Equal(t, schemas.ActionReview, productionResult.Decision.Action)
		assert.Equal(t, schemas.DocumentationImpactPossible, productionResult.Decision.DocumentationImpact)

		// (3) A DIFFERENT shadow binding — still its own namespace: never
		// served by the first shadow binding's entry.
		otherShadowBinding := "shadow:other-binding"
		portOtherShadow := &stageScriptedPort{compiled: compiled}
		stageOtherShadow := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:          portOtherShadow,
			Workflow:      *compiled,
			Memo:          memo,
			MemoNamespace: otherShadowBinding,
		})
		otherShadowResult, err := stageOtherShadow.Decide(stageInputFixture(schemas.DocumentationImpactDefinite))
		require.NoError(t, err)
		require.NotNil(t, otherShadowResult.Decision)
		assert.Equal(t, 1, portOtherShadow.callCount())
		assert.False(t, otherShadowResult.CacheHit)

		// (4) Namespace isolation: shadow-stored decisions are NOT visible to
		// the production namespace; both directions are isolated.
		subset := policy.JevStageMemoDiffSubset(policy.JevChangeRefs{
			Commit:            decisionStageChange.Commit,
			DiffRef:           decisionStageChange.DiffRef,
			ChangeAnalysisRef: decisionStageChange.ChangeAnalysisRef,
		})
		productionKey, err := memo.KeyFor(policy.JevMemoKeyInput{
			DiffSubset:    subset,
			PolicyVersion: 1,
			Namespace:     policy.ProductionNamespace,
		})
		require.NoError(t, err)
		shadowKey, err := memo.KeyFor(policy.JevMemoKeyInput{
			DiffSubset:    subset,
			PolicyVersion: 1,
			Namespace:     shadowBinding,
		})
		require.NoError(t, err)
		assert.NotEqual(t, shadowKey, productionKey)

		fromShadowPartition, err := memo.Get(shadowBinding, shadowKey)
		require.NoError(t, err)
		require.NotNil(t, fromShadowPartition)
		assert.Equal(t, schemas.ActionInvestigate, fromShadowPartition.Action)

		fromProductionPartition, err := memo.Get(policy.ProductionNamespace, productionKey)
		require.NoError(t, err)
		require.NotNil(t, fromProductionPartition)
		assert.Equal(t, schemas.ActionReview, fromProductionPartition.Action)

		// The partitions stay separate (D6): a production lookup with a shadow
		// key finds nothing, and vice versa.
		crossReadShadowKey, err := memo.Get(policy.ProductionNamespace, shadowKey)
		require.NoError(t, err)
		assert.Nil(t, crossReadShadowKey)
		crossReadProductionKey, err := memo.Get(shadowBinding, productionKey)
		require.NoError(t, err)
		assert.Nil(t, crossReadProductionKey)

		assert.Equal(t, policy.JevMemoStats{
			Entries: 3,
			ByNamespace: map[string]int{
				policy.ProductionNamespace: 1,
				shadowBinding:              1,
				otherShadowBinding:         1,
			},
		}, memo.Stats())
	})

	t.Run("memo-failure-never-breaks-the-stage", func(t *testing.T) {
		// An unavailable memo must degrade to the uncached path (fresh results)
		// — it must NEVER break the stage's result.
		port := &stageScriptedPort{compiled: compiled}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
			Memo:     stageFailingMemo{},
		})

		first, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		assert.Equal(t, 1, port.callCount())
		assert.False(t, first.CacheHit)
		require.NotNil(t, first.Telemetry)
		assert.Equal(t, portStageTelemetry, first.Telemetry)

		second, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		assert.Equal(t, 2, port.callCount())
		// Every call is a MISS (the memo is down): the fresh result's model
		// half still carries through, with no cache marker.
		require.NotNil(t, second.Telemetry)
		assert.Equal(t, portStageTelemetry, second.Telemetry)
		assert.False(t, second.CacheHit)
	})

	t.Run("invalid-memo-namespace-fails-closed-before-the-port", func(t *testing.T) {
		// A namespace violation fails closed (D6): the stage surfaces the error
		// instead of silently missing, and the port never runs.
		port := &stageScriptedPort{compiled: compiled}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:          port,
			Workflow:      *compiled,
			Memo:          policy.NewJevMemoCache(),
			MemoNamespace: "staging",
		})

		_, err := stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.Error(t, err)
		var namespaceErr *policy.InvalidMemoNamespaceError
		require.ErrorAs(t, err, &namespaceErr)
		assert.Equal(t, 0, port.callCount())
	})

	t.Run("memo-namespace-violations-at-get-and-set-fail-closed", func(t *testing.T) {
		// D6 at the stage seam: a namespace violation escaping the cache's OWN
		// API — on either the lookup or the store — surfaces instead of
		// degrading to the uncached path.
		getFailure := stageMemoNamespaceFailure{failOnGet: true}
		portGet := &stageScriptedPort{compiled: compiled}
		stageGet := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     portGet,
			Workflow: *compiled,
			Memo:     getFailure,
		})
		_, getErr := stageGet.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.Error(t, getErr)
		var namespaceErr *policy.InvalidMemoNamespaceError
		require.ErrorAs(t, getErr, &namespaceErr)
		// Fail closed BEFORE the port call.
		assert.Equal(t, 0, portGet.callCount())

		setFailure := stageMemoNamespaceFailure{failOnSet: true}
		portSet := &stageScriptedPort{compiled: compiled}
		stageSet := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     portSet,
			Workflow: *compiled,
			Memo:     setFailure,
		})
		_, setErr := stageSet.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.Error(t, setErr)
		require.ErrorAs(t, setErr, &namespaceErr)
		// The store violation surfaces after the port ran (a miss stores only
		// once the runtime produced its decision).
		assert.Equal(t, 1, portSet.callCount())
	})

	t.Run("cross-instance-memo-hit-attributes-this-call-wall-time", func(t *testing.T) {
		// A cache shared BEYOND the stage instance has no store-time result
		// sidecar to return: the hit falls back to truthful this-call
		// attribution from the injected clock (nothing was routed here, so no
		// model/provider half exists to report).
		memo := policy.NewJevMemoCache()
		prime := &stageScriptedPort{compiled: compiled}
		stageOne := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     prime,
			Workflow: *compiled,
			Memo:     memo,
		})
		first, err := stageOne.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)
		require.NotNil(t, first.Decision)

		ticks := []int{900, 1200}
		read := 0
		clock := func() int {
			if read >= len(ticks) {
				t.Fatal("clock exhausted (test bug)")
			}
			value := ticks[read]
			read++
			return value
		}
		shared := &stageScriptedPort{compiled: compiled}
		stageTwo := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     shared,
			Workflow: *compiled,
			Clock:    clock,
			Memo:     memo,
		})
		second, err := stageTwo.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
		require.NoError(t, err)

		// The decision is the SAME cached object; the port never ran.
		assert.Equal(t, 0, shared.callCount())
		assert.True(t, second.CacheHit)
		assert.Same(t, first.Decision, second.Decision)
		// This-call wall-time (900 → 1200) with no routed halves.
		assert.Equal(t, &policy.JevDecisionStageTelemetry{LatencyMS: 300}, second.Telemetry)
	})
}

// stageMemoNamespaceFailure is a memo double that answers the key derivation
// and then fails with the D6 fail-closed namespace vocabulary at the chosen
// surface (Get or Set).
type stageMemoNamespaceFailure struct {
	failOnGet bool
	failOnSet bool
}

var _ policy.JevMemoCachePick = stageMemoNamespaceFailure{}

func (stageMemoNamespaceFailure) KeyFor(input policy.JevMemoKeyInput) (string, error) {
	return "k-namespace", nil
}

func (m stageMemoNamespaceFailure) Get(namespace, key string) (*schemas.Decision, error) {
	if m.failOnGet {
		return nil, &policy.InvalidMemoNamespaceError{
			Namespace: namespace,
			Message:   "invalid memo namespace " + namespace + " (test double)",
		}
	}
	return nil, nil
}

func (m stageMemoNamespaceFailure) Set(namespace, key string, decision *schemas.Decision) error {
	if m.failOnSet {
		return &policy.InvalidMemoNamespaceError{
			Namespace: namespace,
			Message:   "invalid memo namespace " + namespace + " (test double)",
		}
	}
	return nil
}

/* ------------------------------- suite 3: the core bridge (review M1) */

func TestBridgeCoreDecisionStage(t *testing.T) {
	// The schema ChangeAnalysis example record — the bridge's input fixture
	// (TS CHANGE_ANALYSIS_EXAMPLE).
	record := schemas.ChangeAnalysisExample

	t.Run("bridge-builds-jev-input-from-analysis", func(t *testing.T) {
		input := policy.BuildJevStageInput(&record, nil)

		// Change-refs triple — the P5-T06 decision-fixture mapping (commit ←
		// head_sha, diff_ref ← git:<base>..<head>, change_analysis_ref ←
		// run_id).
		assert.Equal(t, policy.JevChangeRefs{
			Commit:            "81af92e",
			DiffRef:           "git:8a73f2c..81af92e",
			ChangeAnalysisRef: "acme/payments#8a73f2c..81af92e",
		}, input.Change)

		// Deterministic grounding — the derivation reproduces the canonical
		// worked example's decision fields (DECISION_EXAMPLE: behavioral /
		// possible / medium).
		context := input.AnalysisContext
		assert.Equal(t, schemas.ChangeKindBehavioral, context.ChangeKind)
		assert.Equal(t, schemas.DocumentationImpactPossible, context.DocumentationImpact)
		assert.Equal(t, schemas.RiskLevelMedium, context.Risk)
		// deep_analysis skipped → the declared V1 grounding confidence.
		assert.Equal(t, 0.3, context.Confidence)

		// Grounding answers keyed to the universal questions, every probability
		// the declared grounding confidence; affected_doc_count honestly 0 (the
		// bare record carries no manifest knowledge — never fabricated).
		assert.Equal(t, policy.JevAnswers{
			"public_behavior_changed": {Value: json.RawMessage("true"), Probability: 0.3},
			"api_contract_changed":    {Value: json.RawMessage("false"), Probability: 0.3},
			"affected_doc_count":      {Value: json.RawMessage("0"), Probability: 0.3},
			"documentation_impact":    {Value: json.RawMessage(`"possible"`), Probability: 0.3},
		}, context.Answers)

		// Honest empty candidate set — manifest knowledge is not derivable
		// from a bare record (D14).
		assert.Equal(t, []string{}, context.Candidates)

		// One machine-resolvable evidence row per changed file (path-level
		// ref; kind api_diff only when the file carries the api_diff signal).
		assert.Equal(t, []schemas.Evidence{
			{
				Kind: schemas.EvidenceKindOther,
				Ref:  "src/payments/refund.ts",
				Note: "change-analysis signal(s): symbols_changed — path-level ref (anchored refs are a declared V1 coverage gap)",
			},
			{
				Kind: schemas.EvidenceKindOther,
				Ref:  "src/payments/refund.test.ts",
				Note: "change-analysis signal(s): test_only — path-level ref (anchored refs are a declared V1 coverage gap)",
			},
		}, context.Evidence)

		// Default untrustedContent = the record's own diff-bearing content
		// surface, ONE block (Phase-2 pattern; containment tagging is the
		// gateway's job — nothing is wrapped here).
		require.Len(t, input.UntrustedContent, 1)
		// JSON.stringify(record) pinned byte-for-byte (struct field order =
		// schema order, no HTML escaping).
		assert.Equal(t, `{"schema":"docsync.change-analysis/v1","run_id":"acme/payments#8a73f2c..81af92e","repo":"acme/payments","base_sha":"8a73f2c","head_sha":"81af92e","files":[{"path":"src/payments/refund.ts","status":"modified","generated":false,"test_only":false,"formatting_only":false,"symbols_changed":true,"api_diff":false,"config_diff":false,"symbols_detail":{"added":[],"removed":[],"changed":[{"name":"idempotencyKey","changeKinds":["type"]}]}},{"path":"src/payments/refund.test.ts","status":"added","test_only":true}],"dependencies_changed":false,"classification_hints":[{"kind":"test_only","note":"all changed paths match src/**/*.test.ts"}],"deep_analysis":{"status":"skipped","reason":"no type-aware analyzer coverage for this language"},"analyzer_coverage":[{"analyzer":"typescript","status":"stub","note":"stub: no AST/symbol extraction in V1 — tree-sitter symbol/type-level analysis lands in Phase 11 (F11-1); per-file symbol diffs are a declared V1 coverage gap"},{"analyzer":"generic","status":"ok"}]}`, input.UntrustedContent[0])

		// The extra-override seam: caller-supplied content REPLACES the derived
		// default (the orchestrator's real diff excerpts are strictly richer).
		untrusted := []string{"diff --git a/src/payments/refund.ts (untrusted repository content)"}
		assert.Equal(t, untrusted, policy.BuildJevStageInput(&record, &policy.BuildJevStageInputExtra{
			UntrustedContent: untrusted,
		}).UntrustedContent)
	})

	t.Run("bridge-null-analysis-abstains-never-guesses", func(t *testing.T) {
		// A deliberately loud stage: any invocation fails the test.
		stage := &bridgeNilStage{}
		bridged := policy.NewBridgeCoreDecisionStage(stage)

		result, err := bridged.Decide(policy.BridgeCoreDecisionStageInput{Analysis: nil})
		require.NoError(t, err)

		assert.Nil(t, result.Decision) // never a fabricated decision
		assert.True(t, result.Abstained)
		require.NotNil(t, result.AbstainReason)
		assert.Equal(t, schemas.AbstainReasonOther, *result.AbstainReason)
		require.NotNil(t, result.Detail)
		assert.Equal(t, policy.NoAnalysisAbstainDetail, *result.Detail)
		// Fail closed BEFORE any model call: the stage (and the port behind it)
		// never ran — a missing record is never guessed into a decision.
		assert.Equal(t, 0, stage.callCount())
	})

	t.Run("bridge-passes-untrusted-content-through", func(t *testing.T) {
		compiled := stageBootstrapCompiled(t)
		port := &stageScriptedPort{
			compiled: compiled,
			result: &policy.JevPortResult{
				Kind:      policy.JevPortResultKindAbstain,
				Reason:    schemas.AbstainReasonModelUnavailable,
				Telemetry: stageTelemetryFixture(),
			},
		}
		bridged := policy.NewBridgeCoreDecisionStage(policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
		}))

		first, err := bridged.Decide(policy.BridgeCoreDecisionStageInput{Analysis: &record})
		require.NoError(t, err)

		assert.Equal(t, 1, port.callCount())
		// The derived diff-bearing content surface reached the stage input (and
		// the port): the record JSON as ONE untrusted block.
		require.Len(t, port.calls, 1)
		assert.Equal(t, []string{stageBridgeRecordJSON(t)}, port.calls[0].UntrustedContent)
		// The stage result passes through verbatim (bridge never reshapes it).
		assert.Nil(t, first.Decision)
		assert.True(t, first.Abstained)
		require.NotNil(t, first.AbstainReason)
		assert.Equal(t, schemas.AbstainReasonModelUnavailable, *first.AbstainReason)

		// Structural seam: the wider core-shaped input ({ ctx, relevance,
		// analysis }) is accepted and ONLY analysis is read (OD-1, no core
		// import) — the excess members are ignored, never consulted.
		coreShaped := stageCoreShapedBridgeInput{
			Analysis:  &record,
			Ctx:       stageCoreCtxSketch{Repo: "org/repo"},
			Relevance: stageCoreRelevanceSketch{Docs: []stageCoreDocSketch{}},
		}
		second, err := bridged.Decide(policy.BridgeCoreDecisionStageInput{Analysis: coreShaped.Analysis})
		require.NoError(t, err)
		assert.Equal(t, 2, port.callCount())
		require.Len(t, port.calls, 2)
		assert.Equal(t, []string{stageBridgeRecordJSON(t)}, port.calls[1].UntrustedContent)
		assert.True(t, second.Abstained)
		require.NotNil(t, second.AbstainReason)
		assert.Equal(t, schemas.AbstainReasonModelUnavailable, *second.AbstainReason)
	})
}

// stageCoreShapedBridgeInput is the wider core-shaped input the TS probe
// passes to the bridge (`{ analysis, ctx, relevance }`): the bridge reads ONLY
// analysis (OD-1 — policy must not import core, so the excess members ride a
// local structural literal).
type stageCoreShapedBridgeInput struct {
	Analysis  *schemas.ChangeAnalysis
	Ctx       stageCoreCtxSketch
	Relevance stageCoreRelevanceSketch
}

// stageBridgeRecordJSON renders the record's JSON.stringify form for the
// pinned block assertions.
func stageBridgeRecordJSON(t *testing.T) string {
	t.Helper()
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	require.NoError(t, encoder.Encode(&schemas.ChangeAnalysisExample))
	return strings.TrimSuffix(buf.String(), "\n")
}

/* ------------------------- the bridge derivation matrix (review M1) */

// TestBuildJevStageInputDerivationMatrix pins the conservative, fail-closed
// ChangeAnalysis → grounding mapping across the branch matrix (the TS bridge
// suite pins the worked example only — behavioral/possible/medium/0.3).
func TestBuildJevStageInputDerivationMatrix(t *testing.T) {
	file := func(path string, flags map[string]bool) schemas.ChangedFile {
		changed := schemas.ChangedFile{Path: path, Status: "modified"}
		if value, ok := flags["generated"]; ok {
			changed.Generated = stageBoolPtr(value)
		}
		if value, ok := flags["test_only"]; ok {
			changed.TestOnly = stageBoolPtr(value)
		}
		if value, ok := flags["formatting_only"]; ok {
			changed.FormattingOnly = stageBoolPtr(value)
		}
		if value, ok := flags["symbols_changed"]; ok {
			changed.SymbolsChanged = stageBoolPtr(value)
		}
		if value, ok := flags["api_diff"]; ok {
			changed.APIDiff = stageBoolPtr(value)
		}
		if value, ok := flags["config_diff"]; ok {
			changed.ConfigDiff = stageBoolPtr(value)
		}
		return changed
	}
	record := func(files []schemas.ChangedFile, dependenciesChanged bool, performed bool) schemas.ChangeAnalysis {
		status := schemas.DeepAnalysisSkipped
		if performed {
			status = schemas.DeepAnalysisPerformed
		}
		return schemas.ChangeAnalysis{
			Schema:              "docsync.change-analysis/v1",
			RunID:               "acme/payments#8a73f2c..81af92e",
			Repo:                "acme/payments",
			BaseSHA:             "8a73f2c",
			HeadSHA:             "81af92e",
			Files:               files,
			DependenciesChanged: dependenciesChanged,
			ClassificationHints: []schemas.ClassificationHint{},
			DeepAnalysis:        schemas.DeepAnalysis{Status: status},
			AnalyzerCoverage:    []schemas.AnalyzerCoverage{},
		}
	}

	cases := []struct {
		name                string
		analysis            schemas.ChangeAnalysis
		changeKind          schemas.ChangeKind
		documentationImpact schemas.DocumentationImpact
		risk                schemas.RiskLevel
		confidence          float64
	}{
		{
			name:                "an empty file set never fabricates none — unknown",
			analysis:            record(nil, false, false),
			changeKind:          schemas.ChangeKindBehavioral,
			documentationImpact: schemas.DocumentationImpactUnknown,
			risk:                schemas.RiskLevelLow,
			confidence:          0.3,
		},
		{
			name:                "trivial formatting-only surface",
			analysis:            record([]schemas.ChangedFile{file("a.md", map[string]bool{"formatting_only": true})}, false, false),
			changeKind:          schemas.ChangeKindFormattingOnly,
			documentationImpact: schemas.DocumentationImpactNone,
			risk:                schemas.RiskLevelLow,
			confidence:          0.3,
		},
		{
			name:                "trivial test-only surface",
			analysis:            record([]schemas.ChangedFile{file("a.test.ts", map[string]bool{"test_only": true})}, false, false),
			changeKind:          schemas.ChangeKindTestOnly,
			documentationImpact: schemas.DocumentationImpactNone,
			risk:                schemas.RiskLevelLow,
			confidence:          0.3,
		},
		{
			name:                "dependency change routes high risk",
			analysis:            record([]schemas.ChangedFile{file("package.json", map[string]bool{})}, true, false),
			changeKind:          schemas.ChangeKindDependency,
			documentationImpact: schemas.DocumentationImpactNone,
			risk:                schemas.RiskLevelHigh,
			confidence:          0.3,
		},
		{
			name:                "api contract change is possible / medium",
			analysis:            record([]schemas.ChangedFile{file("api.ts", map[string]bool{"api_diff": true})}, false, false),
			changeKind:          schemas.ChangeKindAPIContract,
			documentationImpact: schemas.DocumentationImpactPossible,
			risk:                schemas.RiskLevelMedium,
			confidence:          0.3,
		},
		{
			name:                "configuration change is possible / medium",
			analysis:            record([]schemas.ChangedFile{file("config.yml", map[string]bool{"config_diff": true})}, false, false),
			changeKind:          schemas.ChangeKindConfiguration,
			documentationImpact: schemas.DocumentationImpactPossible,
			risk:                schemas.RiskLevelMedium,
			confidence:          0.3,
		},
		{
			name:                "deep analysis performed raises the declared grounding confidence",
			analysis:            record([]schemas.ChangedFile{file("a.ts", map[string]bool{"symbols_changed": true})}, false, true),
			changeKind:          schemas.ChangeKindBehavioral,
			documentationImpact: schemas.DocumentationImpactPossible,
			risk:                schemas.RiskLevelMedium,
			confidence:          0.6,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			analysis := testCase.analysis
			input := policy.BuildJevStageInput(&analysis, nil)
			context := input.AnalysisContext
			assert.Equal(t, testCase.changeKind, context.ChangeKind)
			assert.Equal(t, testCase.documentationImpact, context.DocumentationImpact)
			assert.Equal(t, testCase.risk, context.Risk)
			assert.Equal(t, testCase.confidence, context.Confidence)
			// Every grounding answer probability equals the declared
			// grounding confidence (never calibrated on the fly).
			for _, answer := range context.Answers {
				assert.Equal(t, testCase.confidence, answer.Probability)
			}
		})
	}

	t.Run("api-diff-evidence-rows-name-their-signals", func(t *testing.T) {
		analysis := record([]schemas.ChangedFile{
			file("api.ts", map[string]bool{"api_diff": true, "config_diff": true, "generated": true}),
		}, false, false)
		input := policy.BuildJevStageInput(&analysis, nil)
		require.Len(t, input.AnalysisContext.Evidence, 1)
		evidence := input.AnalysisContext.Evidence[0]
		// Kind api_diff when the file carries the api_diff signal.
		assert.Equal(t, schemas.EvidenceKindAPIDiff, evidence.Kind)
		assert.Equal(t, "api.ts", evidence.Ref)
		assert.Equal(t, "change-analysis signal(s): api_diff, config_diff, generated — path-level ref (anchored refs are a declared V1 coverage gap)", evidence.Note)
	})
}

/* -------- V-P5-4 structural compatibility with the core decision-stage port */

// coreStageTelemetrySketch is core's DecisionStageResult telemetry member
// duplicated as a structural literal (the TS probe sketch).
type coreStageTelemetrySketch struct {
	LatencyMS int
	Model     *string
	Provider  *string
}

// coreStageResultSketch is core's `DecisionStageResult`
// (core/src/orchestrator/types.ts) duplicated as a structural literal —
// @docsync/policy must NOT import @docsync/core (OD-1). A drift in the core
// shape fails the reflect pins below.
type coreStageResultSketch struct {
	Decision      *schemas.Decision
	Abstained     bool
	AbstainReason *schemas.AbstainReason
	Telemetry     *coreStageTelemetrySketch
}

// stageToCoreSketch renders a Jev decision-stage result onto the core sketch —
// the P5-T06 adapter's mapping, pinned here field-by-field.
func stageToCoreSketch(result policy.JevDecisionStageResult) coreStageResultSketch {
	sketch := coreStageResultSketch{
		Decision:  result.Decision,
		Abstained: result.Abstained,
	}
	if result.AbstainReason != nil {
		reason := schemas.AbstainReason(*result.AbstainReason)
		sketch.AbstainReason = &reason
	}
	if result.Telemetry != nil {
		sketch.Telemetry = &coreStageTelemetrySketch{
			LatencyMS: result.Telemetry.LatencyMS,
			Model:     result.Telemetry.Model,
			Provider:  result.Telemetry.Provider,
		}
	}
	return sketch
}

// stageCoreCtxSketch / stageCoreRelevanceSketch sketch the core
// DecisionStagePort input members the TS probe names.
type stageCoreCtxSketch struct {
	Repo  string
	RunID string
	Clock func() string
}

type stageCoreDocSketch struct {
	DocPath string
	Mode    string
}

type stageCoreCapsSketch struct {
	MaxDocs int
}

type stageCoreRelevanceSketch struct {
	Docs []stageCoreDocSketch
	Caps stageCoreCapsSketch
}

type stageCoreDecisionStageInputSketch struct {
	Ctx       stageCoreCtxSketch
	Relevance stageCoreRelevanceSketch
}

// stageCoreDecisionStagePortSketch is core's `DecisionStagePort`, result side
// pinned; ctx side sketched (the orchestrator adapter owns the bridging).
type stageCoreDecisionStagePortSketch interface {
	Decide(input stageCoreDecisionStageInputSketch) (coreStageResultSketch, error)
}

// stageBridgedAdapter is the bridging adapter the orchestrator writes
// (P5-T06): the core input drives the Jev stage, and the RESULT side must
// satisfy the core shape structurally (pinned by stageToCoreSketch).
type stageBridgedAdapter struct {
	stage policy.JevDecisionStage
}

var _ stageCoreDecisionStagePortSketch = stageBridgedAdapter{}

func (a stageBridgedAdapter) Decide(_ stageCoreDecisionStageInputSketch) (coreStageResultSketch, error) {
	// The real adapter derives the Jev input from ctx/relevance; the pin's job
	// is the RESULT side and the decide-method shape.
	result, err := a.stage.Decide(stageInputFixture(schemas.DocumentationImpactPossible))
	if err != nil {
		return coreStageResultSketch{}, err
	}
	return stageToCoreSketch(result), nil
}

// stagePinnedField pins one core-sketch field onto the Jev shape: identical
// types, or — for nested struct members (the TS structural subtyping the
// probes check recursively) — a recursive field pin of the pointed-to
// structs, where extra members on the Jev side are allowed (the forward
// direction) exactly like TS's structural extensibility.
func stagePinnedField(t *testing.T, target reflect.Type, sketchField reflect.StructField) {
	t.Helper()
	targetField, ok := target.FieldByName(sketchField.Name)
	require.True(t, ok, "core sketch field %s is missing on %s", sketchField.Name, target.Name())

	sketchElem, sketchIsPtrToStruct := structElem(sketchField.Type)
	targetElem, targetIsPtrToStruct := structElem(targetField.Type)
	if sketchIsPtrToStruct && targetIsPtrToStruct {
		stagePinnedFields(t, targetElem, sketchElem)
		return
	}
	require.Equal(t, sketchField.Type.String(), targetField.Type.String(),
		"core sketch field %s drifted in type on %s", sketchField.Name, target.Name())
}

// structElem reports the struct element type when t is a struct or a pointer
// to one.
func structElem(t reflect.Type) (reflect.Type, bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		return t, true
	}
	return nil, false
}

// stagePinnedFields asserts that target carries every field of sketch with a
// compatible type — the Go form of the TS `A extends B ? true : false` probe.
func stagePinnedFields(t *testing.T, target reflect.Type, sketch reflect.Type) {
	t.Helper()
	for index := 0; index < sketch.NumField(); index++ {
		stagePinnedField(t, target, sketch.Field(index))
	}
}

func TestV_P5_4_StructuralCompatibilityWithCoreDecisionStagePort(t *testing.T) {
	compiled := stageBootstrapCompiled(t)

	t.Run("structural-compatibility-with-core-decision-stage-port", func(t *testing.T) {
		resultType := reflect.TypeOf(policy.JevDecisionStageResult{})
		telemetryType := reflect.TypeOf(policy.JevDecisionStageTelemetry{})

		// Forward pins: every core result/telemetry member exists on the Jev
		// shapes with a compatible type — recursively for the nested telemetry
		// member (JevDecisionStageResult extends CoreDecisionStageResult).
		stagePinnedFields(t, resultType, reflect.TypeOf(coreStageResultSketch{}))
		stagePinnedFields(t, telemetryType, reflect.TypeOf(coreStageTelemetrySketch{}))

		// Reverse pins: the Jev shapes' REQUIRED members exist on the core
		// sketches (CoreDecisionStageResult extends JevDecisionStageResult).
		// The optional extras (pointers — AbstainReason, Telemetry, Detail —
		// and the additive usage/cacheHit members) may be absent from the
		// core side; CacheHit's false value renders the TS `cacheHit?: true`
		// absent case.
		stagePinnedFields(t, reflect.TypeOf(coreStageResultSketch{}), reflect.TypeOf(struct {
			Decision  *schemas.Decision
			Abstained bool
		}{}))
		stagePinnedFields(t, reflect.TypeOf(coreStageTelemetrySketch{}), reflect.TypeOf(struct {
			LatencyMS int
		}{}))

		// The runtime pin: the bridging adapter compiles against the core
		// port sketch and maps the fields end-to-end.
		port := &stageScriptedPort{compiled: compiled}
		stage := policy.CreateJevDecisionStage(policy.JevDecisionStageDeps{
			Port:     port,
			Workflow: *compiled,
		})
		bridged := stageBridgedAdapter{stage: stage}
		observed, err := bridged.Decide(stageCoreDecisionStageInputSketch{
			Ctx: stageCoreCtxSketch{
				Repo:  "org/repo",
				RunID: "org/repo#base..head",
				Clock: func() string { return "2026-01-01T00:00:00Z" },
			},
			Relevance: stageCoreRelevanceSketch{
				Docs: []stageCoreDocSketch{{DocPath: "docs/payments.md", Mode: "rw"}},
				Caps: stageCoreCapsSketch{MaxDocs: 5},
			},
		})
		require.NoError(t, err)

		assert.False(t, observed.Abstained)
		require.NotNil(t, observed.Decision)
		assert.Equal(t, schemas.ActionReview, observed.Decision.Action)
		require.NotNil(t, observed.Telemetry)
		require.NotNil(t, observed.Telemetry.Model)
		assert.Equal(t, "provider/routed-9", *observed.Telemetry.Model)
		require.NotNil(t, observed.Telemetry.Provider)
		assert.Equal(t, "z-ai", *observed.Telemetry.Provider)
		assert.Equal(t, 120, observed.Telemetry.LatencyMS)
	})
}

/* ------------- m9: schemas ↔ policy outcome-vocabulary mirror pin */

func TestRunOutcomeVocabularyMirror(t *testing.T) {
	t.Run("PIPELINE-OUTCOME-VOCABULARY-EQUALS-POLICY-OUTCOME-VALUES", func(t *testing.T) {
		// schemas cannot import policy (policy depends on schemas), so
		// PipelineOutcomeValues (docsync.run-outcome/v1 wire vocabulary)
		// MIRRORS PolicyOutcomeValues (the Phase-2 engine outcome labels) as
		// the wire-format single source (OD-2). This pin fails on ANY drift in
		// either copy — members AND order.
		// (The TS `Object.isFrozen` pin has no Go equivalent — a package-level
		// var is not frozen; the values + order pin carries the contract.)
		policyValues := make([]string, 0, len(policy.PolicyOutcomeValues))
		for _, value := range policy.PolicyOutcomeValues {
			policyValues = append(policyValues, string(value))
		}
		pipelineValues := make([]string, 0, len(schemas.PipelineOutcomeValues))
		for _, value := range schemas.PipelineOutcomeValues {
			pipelineValues = append(pipelineValues, string(value))
		}
		assert.Equal(t, pipelineValues, policyValues)
		assert.Equal(t, []string{"ignore", "suggest", "auto", "blocked", "review"}, pipelineValues)
	})
}

// compile-time seam pins: the fakes satisfy the port interfaces they stand
// in for.
var (
	_ policy.DecisionEnginePort = (*stageScriptedPort)(nil)
	_ policy.JevMemoCachePick   = stageFailingMemo{}
	_ policy.JevDecisionStage   = (*bridgeNilStage)(nil)
)

// bridgeNilStage is a deliberately loud JevDecisionStage: the no-analysis
// bridge path must return its canonical abstain WITHOUT touching the
// underlying stage (the TS suite passes `null as unknown as JevDecisionStage`;
// a counting, failing stage proves the "never reached" contract more
// strongly).
type bridgeNilStage struct {
	mu    sync.Mutex
	calls int
}

func (s *bridgeNilStage) Decide(input policy.JevDecisionStageInput) (policy.JevDecisionStageResult, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return policy.JevDecisionStageResult{}, errors.New("the no-analysis bridge path must never reach the stage")
}

func (s *bridgeNilStage) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
