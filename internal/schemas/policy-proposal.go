// Ported from packages/schemas/src/v1/policy-proposal.ts: policy proposal
// (P10-T01; ARCH §8.1, CTX §16) — the structured output of the Policy
// Architect.
//
// Every proposal MUST carry counted evidence ("14/23 …") — proposals without
// counts are rejected at validation (ARCH §8.1). This is a LEARNING artifact:
// it is compiled through the Policy Compiler and promoted by humans; it never
// mutates production policy directly (INV-7). The proposal_id and
// contradiction ids are ULIDs; created_at is emitter-supplied (never a
// wall-clock read here).
package schemas

import "github.com/DUDE616/docSync-go/internal/docutil"

// PolicyProposalSchemaID is the literal schema id (POLICY_PROPOSAL_SCHEMA_ID).
const PolicyProposalSchemaID = "docsync.policy-proposal/v1"

// PolicyProposalType is one of the eight ARCH §8.1 proposal types.
type PolicyProposalType string

// The eight ARCH §8.1 proposal types in canonical order.
const (
	PolicyProposalTypeNewDecisionType     PolicyProposalType = "new_decision_type"
	PolicyProposalTypeNewJevQuestion      PolicyProposalType = "new_jev_question"
	PolicyProposalTypeBetterMapping       PolicyProposalType = "better_mapping"
	PolicyProposalTypeRoutingRule         PolicyProposalType = "routing_rule"
	PolicyProposalTypeThreshold           PolicyProposalType = "threshold"
	PolicyProposalTypeEvidenceRequirement PolicyProposalType = "evidence_requirement"
	PolicyProposalTypeNewException        PolicyProposalType = "new_exception"
	PolicyProposalTypeRepositoryConcept   PolicyProposalType = "repository_concept"
)

// PolicyProposalTypeValues lists the eight proposal types in canonical order
// (POLICY_PROPOSAL_TYPE_VALUES).
var PolicyProposalTypeValues = []PolicyProposalType{
	PolicyProposalTypeNewDecisionType, PolicyProposalTypeNewJevQuestion,
	PolicyProposalTypeBetterMapping, PolicyProposalTypeRoutingRule,
	PolicyProposalTypeThreshold, PolicyProposalTypeEvidenceRequirement,
	PolicyProposalTypeNewException, PolicyProposalTypeRepositoryConcept,
}

// IsValidPolicyProposalType reports membership.
func IsValidPolicyProposalType(t string) bool {
	for _, v := range PolicyProposalTypeValues {
		if string(v) == t {
			return true
		}
	}
	return false
}

// PolicyProposalStatus is the proposal lifecycle status.
type PolicyProposalStatus string

// The proposal statuses in canonical order.
const (
	PolicyProposalStatusDraft     PolicyProposalStatus = "draft"
	PolicyProposalStatusValidated PolicyProposalStatus = "validated"
	PolicyProposalStatusInvalid   PolicyProposalStatus = "invalid"
	PolicyProposalStatusQueued    PolicyProposalStatus = "queued"
)

// PolicyProposalStatusValues lists the four statuses in canonical order
// (POLICY_PROPOSAL_STATUS_VALUES).
var PolicyProposalStatusValues = []PolicyProposalStatus{
	PolicyProposalStatusDraft, PolicyProposalStatusValidated,
	PolicyProposalStatusInvalid, PolicyProposalStatusQueued,
}

// IsValidPolicyProposalStatus reports membership.
func IsValidPolicyProposalStatus(s string) bool {
	for _, v := range PolicyProposalStatusValues {
		if string(v) == s {
			return true
		}
	}
	return false
}

// CountedEvidence is the mandatory "14/23 style" counted evidence —
// ratio-checked (ARCH §8.1) (COUNTED_EVIDENCE_SCHEMA).
type CountedEvidence struct {
	Statement    string `json:"statement" yaml:"statement"`
	SupportCount int    `json:"support_count" yaml:"support_count"`
	TotalCount   int    `json:"total_count" yaml:"total_count"`
}

// Validate checks the counted evidence record.
func (c *CountedEvidence) Validate() []Issue {
	var issues []Issue
	Collect(&issues, RequiredString(c.Statement, 1, []string{"statement"}))
	if c.SupportCount < 1 {
		Collect(&issues, &Issue{Path: []string{"support_count"}, Message: "Number must be greater than 0"})
	}
	if c.TotalCount < 1 {
		Collect(&issues, &Issue{Path: []string{"total_count"}, Message: "Number must be greater than 0"})
	}
	return issues
}

// PolicyProposal is the Policy Architect output record
// (POLICY_PROPOSAL_SCHEMA).
type PolicyProposal struct {
	Schema             string               `json:"schema" yaml:"schema"`
	ProposalID         string               `json:"proposal_id" yaml:"proposal_id"`
	CreatedAt          string               `json:"created_at" yaml:"created_at"`
	PolicyVersionBasis int                  `json:"policy_version_basis" yaml:"policy_version_basis"`
	Type               PolicyProposalType   `json:"type" yaml:"type"`
	CountedEvidence    []CountedEvidence    `json:"counted_evidence" yaml:"counted_evidence"`
	Artifact           map[string]any       `json:"artifact" yaml:"artifact"`
	Status             PolicyProposalStatus `json:"status" yaml:"status"`
	Diagnostics        []string             `json:"diagnostics,omitempty" yaml:"diagnostics,omitempty"`
}

// policyProposalAllowedRootKeys is the strict root key set.
var policyProposalAllowedRootKeys = []string{
	"schema", "proposal_id", "created_at", "policy_version_basis", "type",
	"counted_evidence", "artifact", "status", "diagnostics",
}

// Validate checks the proposal, including the counted-evidence ratio check
// and the invalid-status diagnostics requirement (fail closed: no partial
// policies ever reach production — ARCH §8.2).
func (p *PolicyProposal) Validate() []Issue {
	var issues []Issue
	if p.Schema != PolicyProposalSchemaID {
		Collect(&issues, &Issue{Path: []string{"schema"}, Message: "Invalid literal value, expected \"docsync.policy-proposal/v1\""})
	}
	if !docutil.IsValidULID(p.ProposalID) {
		Collect(&issues, &Issue{Path: []string{"proposal_id"}, Message: "Invalid"})
	}
	if !isZuluDateTime(p.CreatedAt) {
		Collect(&issues, &Issue{Path: []string{"created_at"}, Message: "Invalid datetime"})
	}
	if p.PolicyVersionBasis < 1 {
		Collect(&issues, &Issue{Path: []string{"policy_version_basis"}, Message: "Number must be greater than 0"})
	}
	if !IsValidPolicyProposalType(string(p.Type)) {
		Collect(&issues, &Issue{
			Path: []string{"type"},
			Message: "Invalid enum value. Expected 'new_decision_type' | 'new_jev_question' | 'better_mapping' | " +
				"'routing_rule' | 'threshold' | 'evidence_requirement' | 'new_exception' | 'repository_concept', " +
				"received '" + string(p.Type) + "'",
		})
	}
	if len(p.CountedEvidence) < 1 {
		Collect(&issues, &Issue{Path: []string{"counted_evidence"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range p.CountedEvidence {
		for _, iss := range p.CountedEvidence[i].Validate() {
			iss.Path = append([]string{"counted_evidence", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
		if p.CountedEvidence[i].SupportCount > p.CountedEvidence[i].TotalCount {
			Collect(&issues, &Issue{
				Message: "counted evidence \"" + p.CountedEvidence[i].Statement + "\": support_count (" +
					itoa(p.CountedEvidence[i].SupportCount) + ") exceeds total_count (" +
					itoa(p.CountedEvidence[i].TotalCount) + ")",
			})
		}
	}
	if !IsValidPolicyProposalStatus(string(p.Status)) {
		Collect(&issues, &Issue{
			Path: []string{"status"},
			Message: "Invalid enum value. Expected 'draft' | 'validated' | 'invalid' | 'queued', " +
				"received '" + string(p.Status) + "'",
		})
	}
	if p.Status == PolicyProposalStatusInvalid && len(p.Diagnostics) == 0 {
		Collect(&issues, &Issue{
			Message: "status invalid requires compiler diagnostics — no partial policies ever reach production (ARCH §8.2)",
		})
	}
	for i := range p.Diagnostics {
		Collect(&issues, RequiredString(p.Diagnostics[i], 1, []string{"diagnostics", itoa(i)}))
	}
	return issues
}

// ParsePolicyProposal strictly parses and validates YAML/JSON proposal bytes.
// Unknown keys are rejected at the root (zod strictObject semantics).
func ParsePolicyProposal(data []byte) (*PolicyProposal, error) {
	var p PolicyProposal
	if isJSONish(data) {
		if err := decodeJSONStrict(data, &p); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrictYAMLOrJSON(data, &p, policyProposalAllowedRootKeys); err != nil {
			return nil, err
		}
	}
	if issues := p.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &p, nil
}

// PolicyProposalExample returns the canonical example proposal
// (POLICY_PROPOSAL_EXAMPLE).
func PolicyProposalExample() PolicyProposal {
	return PolicyProposal{
		Schema:             PolicyProposalSchemaID,
		ProposalID:         "01HTXSCHEMA1EXAMP1ES000000",
		CreatedAt:          "2026-10-08T12:00:00.000Z",
		PolicyVersionBasis: 1,
		Type:               PolicyProposalTypeThreshold,
		CountedEvidence: []CountedEvidence{
			{
				Statement:    "17/23 reviewer overrides targeted generated-API routing",
				SupportCount: 17,
				TotalCount:   23,
			},
		},
		Artifact: map[string]any{
			"policy_version":  2,
			"threshold_delta": map[string]any{"auto_apply_floor": 0.02},
		},
		Status: PolicyProposalStatusDraft,
	}
}
