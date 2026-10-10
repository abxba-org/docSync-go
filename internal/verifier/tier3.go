package verifier

// Ported from packages/verifier/src/tier3/tier3.ts (P4-T15..T17, T21; PLAN
// §5.4; ARCH §4.8 Tier-3): the Tier-3 orchestrator — composes BOTH tier-3
// checks in canonical order — judge-independence (pure, D13) FIRST, then
// judge-second-pass (the single injected `JudgeRole` call) — one record each,
// ALWAYS both: a check that cannot run emits 'skipped' WITH detail, never
// silently omitted (invariant 10 — provenance or it didn't happen).
//
// Fail-closed posture:
//   - independence FAIL ⇒ tier 'fail', the second pass does NOT run (skipped
//     with the D13 detail), and NO abstain record is fabricated — an
//     independence violation is a reject-grade check failure, not an
//     abstention (the pipeline maps tier fail ⇒ report reject);
//   - `patchedContent === nil` (Tier-1 apply produced no text) ⇒ the second
//     pass is not built/run, tier 'skipped', context empty (the pipeline
//     treats a skipped tier-3 as requiring review);
//   - a thrown port error (TotalInferenceFailure / TransportError — D10: the
//     caller degrades) ⇒ second pass 'skipped' with the typed error recorded
//     and an explicit `model_unavailable` abstention — never a crash, never a
//     fabricated pass.
//
// Verdict mapping (V-P4-10 evidence posture): verdict 'pass' ⇒ check pass;
// 'reject' ⇒ check FAIL with the judge's reasons in the detail AND a judge
// block carrying the same reasons (reject ⇒ report reject); 'review' ⇒ check
// pass with the mandated detail (a review verdict is a legitimate outcome, not
// a failure — the pipeline maps it to report verdict review). The judge block
// pins the FULL model binding (D3): `model` is the ACTUALLY ROUTED model
// (telemetry `routed_model`), plus `requested_model`/`provider` when the
// telemetry carries them. The port's verdict arm carries the validated
// `questions` record when the judge answered the typed questions — the block
// pins it VERBATIM (never fabricated, never dropped), so per-question answers
// survive into the report's Tier-3 evidence trail; the check detail still
// records the asked question ids as an explicit note.
//
// Purity: pure over its inputs except the single injected judge call;
// deterministic — same inputs + same scripted judge response ⇒ deep-equal
// result (telemetry-derived detail uses only stable fields, never latency).

import (
	"errors"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/openrouter"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// questionSurfaceNote is the port-surface note appended to every verdict check
// detail (no fabrication).
var questionSurfaceNote = "(typed questions asked: " + strings.Join(questionIDs, ", ") +
	"; per-question answers are pinned verbatim in the judge block when the judge supplies them)"

// errJudgeNotInjected stands in for the TS thrown TypeError when the judge
// port is absent: the catch block renders it through the plain-Error branch.
var errJudgeNotInjected = errors.New("judge port not injected (fail-closed)")

// secondPassCheck is the canonical second-pass check record (id + tier fixed).
// A nil detail leaves the field absent (TS `detail === undefined ? {} : …`).
func secondPassCheck(status string, detail *string) schemas.VerificationCheck {
	return schemas.VerificationCheck{ID: "judge-second-pass", Status: status, Detail: detail}
}

// reasonsText joins reasons for evidence-bearing details (V-P4-10).
func reasonsText(reasons []string) string {
	return strings.Join(reasons, "; ")
}

// telemetryNote is the deterministic telemetry note (stable fields only —
// never latency).
func telemetryNote(telemetry openrouter.RoleTelemetry) string {
	return "routed_model=" + telemetry.RoutedModel + " attempt=" + strconv.Itoa(telemetry.Attempt) +
		" fallback_used=" + strconv.FormatBool(telemetry.FallbackUsed)
}

// abstainReasonText dereferences the port's abstain reason ("" when the port
// omitted it — nothing is fabricated).
func abstainReasonText(reason *schemas.AbstainReason) string {
	if reason == nil {
		return ""
	}
	return string(*reason)
}

// errorText is the typed-error summary for thrown port failures. The TS
// `GatewayError` branch renders the subclass NAME (TransportError,
// TotalInferenceFailure, …) with the class code; plain errors render as
// `Error: <message>` (TS `error.name: error.message`).
func errorText(err error) string {
	if err == nil {
		return ""
	}
	var transport *openrouter.TransportError
	var total *openrouter.TotalInferenceFailure
	var modelsConfig *openrouter.ModelsConfigError
	var roleBinding *openrouter.RoleBindingError
	var guard *openrouter.GatewayGuardError
	var structured *openrouter.StructuredOutputError
	var notImplemented *openrouter.NotYetImplementedRoleError
	var replayExhausted *openrouter.ReplayExhaustedError
	var gateway *openrouter.GatewayError
	switch {
	case errors.As(err, &transport):
		return "TransportError (" + transport.Code + "): " + transport.Message
	case errors.As(err, &total):
		return "TotalInferenceFailure (" + total.Code + "): " + total.Message
	case errors.As(err, &modelsConfig):
		return "ModelsConfigError (" + modelsConfig.Code + "): " + modelsConfig.Message
	case errors.As(err, &roleBinding):
		return "RoleBindingError (" + roleBinding.Code + "): " + roleBinding.Message
	case errors.As(err, &guard):
		return "GatewayGuardError (" + guard.Code + "): " + guard.Message
	case errors.As(err, &structured):
		return "StructuredOutputError (" + structured.Code + "): " + structured.Message
	case errors.As(err, &notImplemented):
		return "NotYetImplementedRoleError (" + notImplemented.Code + "): " + notImplemented.Message
	case errors.As(err, &replayExhausted):
		return "ReplayExhaustedError (" + replayExhausted.Code + "): " + replayExhausted.Message
	case errors.As(err, &gateway):
		return "GatewayError (" + gateway.Code + "): " + gateway.Message
	default:
		return "Error: " + err.Error()
	}
}

// judgeBlock maps a verdict result to the D3-pinned judge block (questions
// pinned verbatim).
func judgeBlock(result openrouter.JudgeResult) *schemas.VerificationJudge {
	judge := &schemas.VerificationJudge{
		Model:   result.Telemetry.RoutedModel,
		Verdict: schemas.VerificationVerdict(result.Verdict),
	}
	// TS `requested_model === undefined ? {} : …` — the landed Go telemetry
	// field is a plain string, so the absent case renders as "".
	if result.Telemetry.RequestedModel != "" {
		requestedModel := result.Telemetry.RequestedModel
		judge.RequestedModel = &requestedModel
	}
	if result.Telemetry.Provider != nil {
		provider := *result.Telemetry.Provider
		judge.Provider = &provider
	}
	// TS `questions === undefined ? {} : {…questions}` — the record is copied
	// into the schemas answer type, never fabricated, never dropped.
	if result.Questions != nil {
		questions := make(map[string]schemas.VerificationJudgeAnswer, len(result.Questions))
		for id, answer := range result.Questions {
			questions[id] = schemas.VerificationJudgeAnswer{
				Value:       answer.Value,
				Probability: answer.Probability,
			}
		}
		judge.Questions = questions
	}
	// TS `reasons.length === 0 ? {} : {…reasons}` — an empty array stays absent.
	if len(result.Reasons) > 0 {
		reasons := make([]string, len(result.Reasons))
		copy(reasons, result.Reasons)
		judge.Reasons = reasons
	}
	return judge
}

// RunTier3 runs BOTH Tier-3 checks in canonical order and returns the tier
// report with the judge/abstention context. See the module doc for the
// fail-closed postures and the verdict mapping. Pure except the single
// injected judge call: a port error is mapped to the `model_unavailable`
// abstention record, never propagated (the TS try/catch degradation).
func RunTier3(input Tier3Input) Tier3Result {
	/* -- 1. judge independence (pure; ALWAYS emitted first) ------------------ */
	independence := CheckJudgeIndependence(input)

	if independence.Status == schemas.CheckFail {
		skipDetail := IndependenceSkipDetail
		return Tier3Result{
			Tier:    schemas.Tier3,
			Status:  schemas.CheckFail,
			Checks:  []schemas.VerificationCheck{independence, secondPassCheck(schemas.CheckSkipped, &skipDetail)},
			Context: Tier3Context{},
		}
	}

	/* -- 2. judge second pass (skipped without patched text — nothing to judge) */
	if input.PatchedContent == nil {
		skipDetail := PatchedContentSkipDetail
		return Tier3Result{
			Tier:    schemas.Tier3,
			Status:  schemas.CheckSkipped,
			Checks:  []schemas.VerificationCheck{independence, secondPassCheck(schemas.CheckSkipped, &skipDetail)},
			Context: Tier3Context{},
		}
	}

	var (
		result openrouter.JudgeResult
		err    error
	)
	if input.Judge == nil {
		// A missing port is the Go stand-in for the TS thrown TypeError on an
		// absent port: degrade through the SAME model_unavailable path, never
		// panic (D10 fail-closed).
		err = errJudgeNotInjected
	} else {
		result, err = input.Judge.Judge(BuildJudgeInvocation(input))
	}
	if err != nil {
		/* -- thrown port error: fail-closed degradation, never a crash ----- */
		detail := "judge invocation failed: " + errorText(err)
		return Tier3Result{
			Tier:   schemas.Tier3,
			Status: schemas.CheckSkipped,
			Checks: []schemas.VerificationCheck{independence, secondPassCheck(schemas.CheckSkipped, &detail)},
			Context: Tier3Context{JudgeAbstain: &schemas.VerificationJudgeAbstain{
				Reason: string(schemas.AbstainReasonModelUnavailable),
				Detail: detail,
			}},
		}
	}

	if result.Kind == openrouter.JudgeKindVerdict {
		judge := judgeBlock(result)
		var check schemas.VerificationCheck
		switch result.Verdict {
		case openrouter.JudgeVerdictReject:
			detail := "judge rejected the patch — " + reasonsText(result.Reasons) + " " + questionSurfaceNote
			check = secondPassCheck(schemas.CheckFail, &detail)
		case openrouter.JudgeVerdictPReview:
			detail := "judge routed the patch to review — " + reasonsText(result.Reasons) + " " + questionSurfaceNote
			check = secondPassCheck(schemas.CheckPass, &detail)
		default:
			detail := "independent judge second pass: pass " + questionSurfaceNote
			check = secondPassCheck(schemas.CheckPass, &detail)
		}
		status := schemas.CheckPass
		if check.Status == schemas.CheckFail {
			status = schemas.CheckFail
		}
		return Tier3Result{
			Tier:    schemas.Tier3,
			Status:  status,
			Checks:  []schemas.VerificationCheck{independence, check},
			Context: Tier3Context{Judge: judge},
		}
	}

	/* -- abstain: first-class degradation (invariant 9), never a pass -------- */
	reason := abstainReasonText(result.Reason)
	detail := "judge abstained (" + reason + "): " + telemetryNote(result.Telemetry)
	return Tier3Result{
		Tier:   schemas.Tier3,
		Status: schemas.CheckSkipped,
		Checks: []schemas.VerificationCheck{independence, secondPassCheck(schemas.CheckSkipped, &detail)},
		Context: Tier3Context{JudgeAbstain: &schemas.VerificationJudgeAbstain{
			Reason: reason,
			Detail: detail,
		}},
	}
}
