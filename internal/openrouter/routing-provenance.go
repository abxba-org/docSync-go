package openrouter

// Ported from packages/openrouter/src/routing/provenance.ts (P12-T12; ARCH
// §10.6; invariant 10) — actual routed-model provenance gates: a decision,
// writer or verifier record naming a model id that is MISSING or FABRICATED
// (neither the telemetry-routed id nor — when telemetry is absent — the
// configured passthrough) is REJECTED, so downstream provenance/honesty
// invariants cannot be fed a lie. "Provenance or it didn't happen."
//
// Why it lives in this package: the model-access layer is the ONLY place that
// knows the ACTUAL routed model — the response-body `model` field is the
// authority, not the models.yml binding. When telemetry carries
// `routed_model`, THAT is the expected id (every record naming a model must
// name the routed id); when telemetry does NOT carry it, the expected id is
// the CONFIGURED model (the declared deviation: telemetry absent) and a record
// naming anything else is rejected. Both branches are fail-closed.
//
// Pure and deterministic: NO network, NO clock, NO randomness — the caller
// supplies the evidence and the claims.

import (
	"strconv"
	"strings"
)

// RoutedModelTelemetry is the telemetry slice this gate consumes: the
// ACTUALLY ROUTED model id, when the completion carried one (TS
// `telemetry?: { readonly routed_model?: string }`).
type RoutedModelTelemetry struct {
	// RoutedModel is the response-body `model` field (the authority —
	// V-P2-11). An empty value means "absent": the configured-passthrough
	// branch applies.
	RoutedModel string `json:"routed_model,omitempty"`
}

// ActualModelEvidence is the role's evidence: the configured model id for the
// call plus the telemetry that carried the actual routed id, if any.
type ActualModelEvidence struct {
	Role            string
	ConfiguredModel string
	Telemetry       *RoutedModelTelemetry
}

// ProvenanceModelClaim is one "a record names a model id" claim.
type ProvenanceModelClaim struct {
	// Role is the role the claim covers.
	Role string
	// NamedModel is the model id the record names; absent/empty is a
	// violation (a record with no model id is not provenance).
	NamedModel *string
}

// ActualModelExpectation is what each role's record MUST name (TS
// ActualModelExpectation).
type ActualModelExpectation struct {
	// Role is the role this expectation covers.
	Role string
	// ExpectedModel is the id the record must name.
	ExpectedModel string
	// TelemetryAuthoritative is true when the expectation came from
	// telemetry's routed model (false → the configured-passthrough branch).
	TelemetryAuthoritative bool
}

// expectedModelFor is the single expectation rule (TS expectedModelFor). When
// telemetry carried `routed_model` it is the authority — every record naming a
// model must name the ACTUAL routed model; when telemetry is absent (or
// carried no routed id) the expected id is the CONFIGURED one, the declared
// deviation. `ok` is false when there is no id to expect at all.
func expectedModelFor(
	evidence ActualModelEvidence,
) (ActualModelExpectation, bool) {
	if evidence.Telemetry != nil && evidence.Telemetry.RoutedModel != "" {
		return ActualModelExpectation{
			Role:                   evidence.Role,
			ExpectedModel:          evidence.Telemetry.RoutedModel,
			TelemetryAuthoritative: true,
		}, true
	}
	if evidence.ConfiguredModel == "" {
		return ActualModelExpectation{}, false
	}
	return ActualModelExpectation{Role: evidence.Role, ExpectedModel: evidence.ConfiguredModel}, true
}

// ResolveActualModelExpectations computes the expectation per evidence row
// (TS resolveActualModelExpectations).
func ResolveActualModelExpectations(
	evidence []ActualModelEvidence,
) []ActualModelExpectation {
	expectations := make([]ActualModelExpectation, 0, len(evidence))
	for _, evidenceRow := range evidence {
		if expectation, ok := expectedModelFor(evidenceRow); ok {
			expectations = append(expectations, expectation)
		}
	}
	return expectations
}

// AssertActualModelProvenance enforces the gate (TS
// assertActualModelProvenance): every model-naming record must name the
// ACTUAL id for its role — a record that names NO id, or names a different id,
// is a VIOLATION (records are rejected fail-closed; provenance or it didn't
// happened — §3 rule 10; invariant 10). A role with evidence but no record
// naming a model for it is also a violation. ALL violations are collected
// before the typed error is raised.
func AssertActualModelProvenance(
	claims []ProvenanceModelClaim,
	evidence []ActualModelEvidence,
) error {
	violations := make([]string, 0, 4)
	evidenceByRole := make(map[string]ActualModelEvidence, len(evidence))
	for _, evidenceRow := range evidence {
		if _, duplicate := evidenceByRole[evidenceRow.Role]; duplicate {
			violations = append(violations, "duplicate evidence for role "+jsonString(evidenceRow.Role))
		}
		evidenceByRole[evidenceRow.Role] = evidenceRow
	}
	claimedRoles := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if _, duplicate := claimedRoles[claim.Role]; duplicate {
			violations = append(violations, "duplicate provenance claim for role "+jsonString(claim.Role))
		}
		claimedRoles[claim.Role] = struct{}{}
		roleEvidence, hasEvidence := evidenceByRole[claim.Role]
		if !hasEvidence {
			violations = append(violations, "role "+jsonString(claim.Role)+
				" has a provenance claim but no telemetry/configured evidence — unverifiable provenance fails closed")
			continue
		}
		expectation, ok := expectedModelFor(roleEvidence)
		if !ok {
			violations = append(violations, "role "+jsonString(claim.Role)+
				" evidence has an empty configuredModel — no id exists to name")
			continue
		}
		namedSuffix := expectationSuffix(expectation)
		if claim.NamedModel == nil || *claim.NamedModel == "" {
			violations = append(violations, "role "+jsonString(claim.Role)+
				" record names NO model id — provenance or it didn't happen (§3 rule 10; ARCH §10.6); expected "+
				jsonString(expectation.ExpectedModel)+namedSuffix)
			continue
		}
		if *claim.NamedModel != expectation.ExpectedModel {
			violations = append(violations, "role "+jsonString(claim.Role)+" record names "+
				jsonString(*claim.NamedModel)+" but the ACTUAL routed model is "+
				jsonString(expectation.ExpectedModel)+mismatchSuffix(expectation))
		}
	}
	for _, evidenceRow := range evidence {
		if _, claimed := claimedRoles[evidenceRow.Role]; !claimed {
			violations = append(violations, "role "+jsonString(evidenceRow.Role)+
				" has evidence but no record names a model for it")
		}
	}
	if len(violations) > 0 {
		return NewActualModelProvenanceError(
			"actual-model provenance enforcement failed ("+strconv.Itoa(len(violations))+
				" violation(s)) — records MUST name the actual routed model ids (P12-T12; ARCH §10.6): "+
				strings.Join(violations, " | "),
			violations)
	}
	return nil
}

// expectationSuffix is the message suffix for an un-named record (the
// telemetry branch is the authority; the passthrough branch names the declared
// deviation).
func expectationSuffix(expectation ActualModelExpectation) string {
	if expectation.TelemetryAuthoritative {
		return " (telemetry-authoritative routed id)"
	}
	return " (configured passthrough — declared deviation: telemetry absent)"
}

// mismatchSuffix is the message suffix for a FABRICATED id: the record named a
// model the call never routed (ARCH §10.6).
func mismatchSuffix(expectation ActualModelExpectation) string {
	if expectation.TelemetryAuthoritative {
		return " (telemetry-authoritative routed id) — fabricated provenance fails closed (ARCH §10.6)"
	}
	return " (configured passthrough — declared deviation: telemetry absent)"
}
