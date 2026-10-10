package verifier

// Ported from packages/verifier/src/tier3/independence.ts (P4-T16; ARCH §4.8
// Tier-3, §10.2/§10.3): the D13 judge-independence check — the judge session
// instance must never be the patch-writer session instance that produced the
// patch, and a declared `must_not_equal_patch-writer_instance` binding
// constraint (models.yml) must not be violated at the binding level.
//
// Violation semantics (fail-closed):
//   - the SAME session id on writer and judge identities is always a FAIL —
//     the same instance judged its own work;
//   - a DECLARED binding constraint collides when the identities collide at
//     the binding level — with the orchestrator-supplied surface that is the
//     same session AND the same model. Model equality alone with distinct
//     session ids is a WARNING-grade concern (the constraint is
//     instance-level), recorded as a note in the pass detail, never a fail.
//
// Telemetry hygiene (invariant 10 posture): every detail states the compared
// identities (role, model, sessionId) with session ids REDACTED to their first
// 8 characters.
//
// Pure and deterministic: no ports, no I/O, no clock.

import "github.com/DUDE616/docSync-go/internal/schemas"

// PASS_DETAIL — the canonical pass detail (mandated wording).
const passDetail = "judge instance independent of the writer instance (D13)"

// IndependenceSkipDetail — the second-decision-pass skip detail when
// independence fails (mandated wording; TS INDEPENDENCE_SKIP_DETAIL).
const IndependenceSkipDetail = "judge independence violated — the second decision pass does not run (D13 fail-closed)"

// redact redacts a session id to its first 8 characters (telemetry hygiene).
func redact(sessionID string) string {
	if len(sessionID) < 8 {
		return sessionID
	}
	return sessionID[:8]
}

// identityLabel is the `role=<r> model=<m> session=<first-8>` identity summary
// for details.
func identityLabel(identity SessionIdentity) string {
	return "role=" + identity.Role + " model=" + identity.Model + " session=" + redact(identity.SessionID)
}

// CheckJudgeIndependence emits the `judge-independence` check (tier 3): FAIL
// on a session-instance collision or on a declared binding-constraint
// collision, PASS otherwise — with the model-equality warning note when the
// constraint is declared and only the model matches across distinct sessions.
// Pure.
func CheckJudgeIndependence(input Tier3Input) schemas.VerificationCheck {
	writer := input.WriterIdentity
	judge := input.JudgeIdentity
	sameSession := writer.SessionID == judge.SessionID
	sameModel := writer.Model == judge.Model
	constraintDeclared := input.BindingConstraint == bindingConstraintPatchWriterInstance
	identities := "writer (" + identityLabel(writer) + ") vs judge (" + identityLabel(judge) + ")"

	if sameSession {
		detail := "judge independence violated: the judge session instance IS the patch-writer session instance " +
			"(same session id): " + identities
		if constraintDeclared && sameModel {
			detail = "judge independence violated: the judge session instance IS the patch-writer session instance " +
				"(same session AND same model — declared binding constraint " +
				"'must_not_equal_patch-writer_instance' broken): " + identities
		}
		return schemas.VerificationCheck{ID: "judge-independence", Status: schemas.CheckFail, Detail: &detail}
	}

	if constraintDeclared && sameModel {
		detail := passDetail + "; note: judge model equals the writer model (" + judge.Model + ") across " +
			"distinct sessions — the constraint is instance-level, so this is a warning-grade " +
			"binding-collision concern, not a violation: " + identities
		return schemas.VerificationCheck{ID: "judge-independence", Status: schemas.CheckPass, Detail: &detail}
	}

	// The mandated pass detail (copied so the check record owns its pointer).
	detail := passDetail
	return schemas.VerificationCheck{ID: "judge-independence", Status: schemas.CheckPass, Detail: &detail}
}
