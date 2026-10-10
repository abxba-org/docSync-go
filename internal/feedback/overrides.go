// Override aggregation (P10-T06; ARCH §8.7, §11.4; CTX §23; PLAN §11.3 F10-4).
//
// Ported from core/src/feedback/overrides.ts. Repeated IDENTICAL corrections —
// same doc path, same normalized correction statement (e.g. 17 reviewer
// overrides of "src/generated/** is not documentation-relevant") — aggregate
// into counted evidence and surface as a QUEUED Policy-Architect proposal
// artifact while the reviewer's click is acknowledged instantly.
//
// INVARIANT (INV-7): feedback accelerates proposals; it NEVER bypasses
// evaluation. The emitted proposal has status `queued` — it still has to be
// compiled, offline-evaluated and promoted by humans (models propose; humans
// promote); no path exists from this module to production policy.
//
// Correction signals: `reject` (statement = reject reason), `edit`
// (statement = the edit diff) and `always_ignore` (statement = the literal
// override marker `always_ignore`). `accept`/`mark_as_intentional` are not
// corrections and never aggregate. Statements are normalized (whitespace
// collapsed, case-folded) so "Same  wording " and "same wording" group
// together; the grouping key is `(doc_path, statement)` with a missing
// `doc_path` grouping under its own null bucket (a statement observed without
// a doc path is never merged with one observed with a path — fail conservative).
// Groups spanning multiple policy versions fail (fail closed): counted
// evidence across versions is not sound evidence.

package feedback

import (
	"fmt"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ALWAYS_IGNORE_STATEMENT is the literal override statement for `always_ignore`
// rows (TS ALWAYS_IGNORE_STATEMENT).
const ALWAYS_IGNORE_STATEMENT = "always_ignore"

// OVERRIDE_ACKNOWLEDGMENT is the instant acknowledgment text (INV-7): the click
// is confirmed, nothing more (TS OVERRIDE_ACKNOWLEDGMENT).
const OVERRIDE_ACKNOWLEDGMENT = "recorded — a reviewable policy proposal will follow (INV-7: feedback never bypasses evaluation)"

// OVERRIDE_PROPOSAL_THRESHOLD is the minimum identical observations before a
// proposal is queued (F10-4: "repeated").
const OVERRIDE_PROPOSAL_THRESHOLD = 2

// collapseWhitespaceFold is the shared core of the TS `.replace(/\s+/g, ' ')`
// normalizers: whitespace runs collapse to single spaces, the ends are
// trimmed, and the result is case-folded (TS `normalizeCorrectionStatement`);
// without the fold it backs IsDocAlreadyCorrect (TS `isDocAlreadyCorrect`).
func collapseWhitespaceFold(s string) string {
	return strings.ToLower(collapseWhitespace(s))
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// NormalizeCorrectionStatement normalizes a correction statement for grouping
// (collapse + case-fold; TS normalizeCorrectionStatement).
func NormalizeCorrectionStatement(statement string) string {
	return collapseWhitespaceFold(statement)
}

// CorrectionStatementOf returns the normalized correction statement of one
// feedback event (`”` for non-corrections; TS correctionStatementOf).
func CorrectionStatementOf(event schemas.FeedbackEvent) string {
	switch event.Signal {
	case schemas.FeedbackEventSignalReject:
		return NormalizeCorrectionStatement(deref(event.RejectReason))
	case schemas.FeedbackEventSignalEdit:
		return NormalizeCorrectionStatement(deref(event.EditDiff))
	case schemas.FeedbackEventSignalAlwaysIgnore:
		return ALWAYS_IGNORE_STATEMENT
	default:
		return ""
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// OverrideAcknowledgment is one aggregated override group: counted evidence +
// the queued proposal (TS OverrideAcknowledgment).
type OverrideAcknowledgment struct {
	// Statement is the normalized correction statement the group aggregates.
	Statement string
	// DocPath is the doc path the corrections target (nil when none carried
	// one — TS null).
	DocPath *string
	// Count is the number of identical observations in the group (the support
	// count).
	Count int
	// Acknowledgment is the instant acknowledgment text (INV-7;
	// OVERRIDE_ACKNOWLEDGMENT).
	Acknowledgment string
	// Proposal is the queued POLICY-PROPOSAL artifact (validated against the
	// schema).
	Proposal schemas.PolicyProposal
}

// AggregateOverridesInput is the input of AggregateOverrides (TS
// AggregateOverridesInput).
type AggregateOverridesInput struct {
	// Rows are the feedback events to aggregate.
	Rows []schemas.FeedbackEvent
	// At is the ISO-8601 instant for `created_at` (injected clock value, UTC
	// `Z`).
	At string
	// ProposalIDs are optional pre-assigned proposal ids (one per queued
	// group, in order).
	ProposalIDs []string
}

// overrideGroup is one aggregated (doc_path, statement) bucket. Groups keep
// first-observation order so the emitted acknowledgments are deterministic
// (TS relied on Map insertion order).
type overrideGroup struct {
	docPath       *string
	statement     string
	policyVersion int
	count         int
}

// AggregateOverrides groups identical corrections and, for every group with at
// least OVERRIDE_PROPOSAL_THRESHOLD observations, emits the instant
// acknowledgment plus a queued, schema-validated policy-proposal artifact
// carrying the counted evidence (`support_count` = group count, `total_count`
// = ALL correction observations in the call). Fewer than the threshold
// observations aggregate silently (no acknowledgment, no proposal). Fail
// closed: a proposal that fails POLICY_PROPOSAL_SCHEMA validation or a group
// spanning policy versions fails instead of emitting an unsound artifact.
func AggregateOverrides(input AggregateOverridesInput) ([]OverrideAcknowledgment, error) {
	groups := map[string]*overrideGroup{}
	order := []string{}
	totalObservations := 0

	for _, row := range input.Rows {
		statement := CorrectionStatementOf(row)
		if len(statement) == 0 {
			continue
		}
		docPath := row.DocPath
		key := fmt.Sprintf("%s\x00%s", deref(docPath), statement)
		existing, ok := groups[key]
		if !ok {
			groups[key] = &overrideGroup{docPath: docPath, statement: statement, policyVersion: row.PolicyVersion, count: 1}
			order = append(order, key)
		} else {
			if existing.policyVersion != row.PolicyVersion {
				return nil, newFeedbackValidationError(fmt.Sprintf(
					"override group (%s) spans policy versions %d and %d — counted evidence across versions is refused (fail closed)",
					existing.statement, existing.policyVersion, row.PolicyVersion))
			}
			existing.count++
		}
		totalObservations++
	}

	acknowledgments := []OverrideAcknowledgment{}
	for _, key := range order {
		group := groups[key]
		if group.count < OVERRIDE_PROPOSAL_THRESHOLD {
			continue
		}
		countedEvidence := []schemas.CountedEvidence{
			{
				Statement: fmt.Sprintf("%d/%d identical reviewer overrides%s: \"%s\"",
					group.count, totalObservations, onDocPathSuffix(group.docPath), group.statement),
				SupportCount: group.count,
				TotalCount:   totalObservations,
			},
		}
		proposalID := ""
		if len(input.ProposalIDs) > len(acknowledgments) {
			proposalID = input.ProposalIDs[len(acknowledgments)]
		} else {
			generated, err := generateID("policy proposal")
			if err != nil {
				return nil, err
			}
			proposalID = generated
		}
		artifact := map[string]any{
			"kind":      "reviewer_override_aggregation",
			"statement": group.statement,
		}
		if group.docPath != nil {
			artifact["doc_path"] = *group.docPath
		}
		candidate := map[string]any{
			"schema":               schemas.PolicyProposalSchemaID,
			"proposal_id":          proposalID,
			"created_at":           input.At,
			"policy_version_basis": group.policyVersion,
			"type":                 string(schemas.PolicyProposalTypeRoutingRule),
			"counted_evidence":     countedEvidence,
			"artifact":             artifact,
			"status":               string(schemas.PolicyProposalStatusQueued),
		}
		proposal, err := parsePolicyProposalCandidate(candidate)
		if err != nil {
			return nil, err
		}
		acknowledgments = append(acknowledgments, OverrideAcknowledgment{
			Statement:      group.statement,
			DocPath:        group.docPath,
			Count:          group.count,
			Acknowledgment: OVERRIDE_ACKNOWLEDGMENT,
			Proposal:       *proposal,
		})
	}
	return acknowledgments, nil
}

// onDocPathSuffix renders the " on <doc path>" clause of the counted-evidence
// statement (empty for the null bucket).
func onDocPathSuffix(docPath *string) string {
	if docPath == nil {
		return ""
	}
	return fmt.Sprintf(" on %s", *docPath)
}

// parsePolicyProposalCandidate validates the queued-proposal candidate against
// POLICY_PROPOSAL_SCHEMA (strict) — the Go form of the TS
// `POLICY_PROPOSAL_SCHEMA.safeParse(candidate)`.
func parsePolicyProposalCandidate(candidate map[string]any) (*schemas.PolicyProposal, error) {
	data, err := marshalRowCompact(candidate)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"queued policy proposal refused (fail closed): %v", err))
	}
	proposal, err := schemas.ParsePolicyProposal(data)
	if err != nil {
		return nil, newFeedbackValidationError(fmt.Sprintf(
			"queued policy proposal refused (fail closed): %s", err))
	}
	return proposal, nil
}
