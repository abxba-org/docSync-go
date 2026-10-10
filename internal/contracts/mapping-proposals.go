package contracts

import "github.com/DUDE616/docSync-go/internal/docutil"

// MappingProposalFields are the rich MappingRecord fields a mapping proposal can
// target (P11-T09). Ported verbatim from mapping-proposals.ts.
var MappingProposalFields = []string{"apis", "configs", "workflows"}

// MappingProposalField is one proposal target field.
type MappingProposalField string

const (
	MappingProposalFieldAPIs      MappingProposalField = "apis"
	MappingProposalFieldConfigs   MappingProposalField = "configs"
	MappingProposalFieldWorkflows MappingProposalField = "workflows"
)

// MappingProposalEvidence is one counted, deterministic evidence record backing
// a suggestion (review input).
type MappingProposalEvidence struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// MappingProposalSuggestion is one graph-refinement suggestion (caller-derived,
// evidence-backed).
type MappingProposalSuggestion struct {
	// DocPath is the manifest document path whose mapping should gain the ref.
	DocPath string
	Field   MappingProposalField
	// Ref is the api/config/workflow ref to ADD to the mapping.
	Ref      string
	Evidence MappingProposalEvidence
}

// MappingProposal is one reviewable mapping-proposal artifact (INV-3 — never
// auto-applied). Status is always MappingProposalStatusProposed.
type MappingProposal struct {
	ProposalID string               `json:"proposalId"`
	DocPath    string               `json:"docPath"`
	Field      MappingProposalField `json:"field"`
	// AddRefs are the refs to add, deduped, in suggestion order.
	AddRefs  []string                  `json:"addRefs"`
	Evidence []MappingProposalEvidence `json:"evidence"`
	// Status is always MappingProposalStatusProposed — ONLY a human gate advances it.
	Status string `json:"status"`
}

// MappingProposalStatusProposed is the only status a derived proposal carries
// (INV-3: no code path advances it past human review).
const MappingProposalStatusProposed = "proposed"

// CodeMappingProposalInput is the distinguishable error code for an unusable
// mapping-proposal input (fail closed). Preserves the observable TS code
// EDOCSYNC_MAPPING_PROPOSAL_INPUT (see conventions / docutil).
const CodeMappingProposalInput = "EDOCSYNC_MAPPING_PROPOSAL_INPUT"

// MappingProposalInputError mirrors the TS MappingProposalInputError: a typed,
// coded error whose message is `mapping proposal: <detail>`.
type MappingProposalInputError struct {
	Detail string
	inner  *docutil.Error
}

func newMappingProposalInputError(detail string) *MappingProposalInputError {
	return &MappingProposalInputError{
		Detail: detail,
		inner:  docutil.New(CodeMappingProposalInput, "mapping proposal: "+detail),
	}
}

// Error renders the coded error (`EDOCSYNC_MAPPING_PROPOSAL_INPUT: <message>`).
func (e *MappingProposalInputError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_MAPPING_PROPOSAL_INPUT.
func (e *MappingProposalInputError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *MappingProposalInputError) Unwrap() error { return e.inner }

// DeriveMappingProposalsInput is the derive input (INV-3: graph MUST be
// error-free; suggestions are deterministic input order; proposalIds and
// generateID are optional injection points for determinism).
type DeriveMappingProposalsInput struct {
	// Graph is the composed document graph (MUST be error-free — proposals refine
	// a usable graph).
	Graph *Graph
	// Suggestions are evidence-backed refinement suggestions (deterministic order).
	Suggestions []MappingProposalSuggestion
	// ProposalIDs are optional pre-assigned ids (one per derived proposal, in
	// derivation order). Omitted → GenerateID is used.
	ProposalIDs []string
	// GenerateID optionally overrides ULID generation (injected for tests /
	// determinism). Omitted → a fresh docutil ULID generator reading the wall clock.
	GenerateID func() (string, error)
}

// group accumulates the refs + evidence for one (docPath, field) proposal group.
type proposalGroup struct {
	DocPath  string
	Field    MappingProposalField
	Refs     []string
	Evidence []MappingProposalEvidence
}

// refsForField returns the target mapping field's current refs (nil for an
// unknown field — trusted at the type level like the TS union).
func refsForField(mapping *MappingRecord, field MappingProposalField) []string {
	switch field {
	case MappingProposalFieldAPIs:
		return mapping.Apis
	case MappingProposalFieldConfigs:
		return mapping.Configs
	case MappingProposalFieldWorkflows:
		return mapping.Workflows
	default:
		return nil
	}
}

// DeriveMappingProposals derives reviewable mapping-proposal artifacts from
// refinement suggestions. Deterministic: proposals are grouped by (docPath,
// field) in first-suggestion order, then sorted by (docPath, field); refs keep
// suggestion order, deduped; refs already present on the target mapping are
// skipped. NEVER mutates the graph (INV-3) — the returned artifacts are the
// ONLY output.
//
// Fail closed: a graph with composition errors, an empty ref, or a suggestion
// for a doc absent from the graph each yields a *MappingProposalInputError.
func DeriveMappingProposals(input DeriveMappingProposalsInput) ([]MappingProposal, error) {
	generateID := input.GenerateID
	if generateID == nil {
		gen := docutil.NewULIDGenerator()
		generateID = gen.Generate
	}

	if len(input.Graph.Errors) > 0 {
		kinds := make([]string, 0, len(input.Graph.Errors))
		for _, entry := range input.Graph.Errors {
			kinds = append(kinds, string(entry.Kind))
		}
		return nil, newMappingProposalInputError(
			"graph carries composition errors — unusable for proposals (fail closed, D8): " + joinComma(kinds),
		)
	}

	for index, suggestion := range input.Suggestions {
		if len(suggestion.Ref) == 0 {
			return nil, newMappingProposalInputError(
				"suggestion " + itoaInt(index) + ": ref must be non-empty",
			)
		}
		if !isMappingProposalField(suggestion.Field) {
			return nil, newMappingProposalInputError(
				"suggestion " + itoaInt(index) + `: field "` + string(suggestion.Field) +
					`" is not a mapping-proposal field — expected one of apis, configs, workflows`,
			)
		}
		if ForwardLookup(input.Graph, suggestion.DocPath) == nil {
			return nil, newMappingProposalInputError(
				"suggestion " + itoaInt(index) + `: doc "` + suggestion.DocPath +
					`" is not in the graph — proposals refine EXISTING mappings, never invent docs`,
			)
		}
	}

	// Group accepted suggestions by (docPath, field), first-occurrence order.
	groupByKey := map[proposalGroupKey]*proposalGroup{}
	order := []proposalGroupKey{}
	for _, suggestion := range input.Suggestions {
		mapping := ForwardLookup(input.Graph, suggestion.DocPath)
		if mapping == nil {
			continue // unreachable — validated above
		}
		if containsString(refsForField(mapping, suggestion.Field), suggestion.Ref) {
			continue
		}
		key := proposalGroupKey{docPath: suggestion.DocPath, field: suggestion.Field}
		group := groupByKey[key]
		if group == nil {
			group = &proposalGroup{DocPath: suggestion.DocPath, Field: suggestion.Field}
			groupByKey[key] = group
			order = append(order, key)
		}
		if !containsString(group.Refs, suggestion.Ref) {
			group.Refs = append(group.Refs, suggestion.Ref)
			group.Evidence = append(group.Evidence, suggestion.Evidence)
		}
	}

	// Sort groups by (docPath, field) — deterministic proposal order.
	ordered := make([]*proposalGroup, 0, len(order))
	for _, key := range order {
		ordered = append(ordered, groupByKey[key])
	}
	sortProposalGroups(ordered)

	proposals := make([]MappingProposal, 0, len(ordered))
	for index, group := range ordered {
		proposalID := ""
		if index < len(input.ProposalIDs) {
			proposalID = input.ProposalIDs[index]
		} else {
			id, err := generateID()
			if err != nil {
				return nil, err
			}
			proposalID = id
		}
		proposals = append(proposals, MappingProposal{
			ProposalID: proposalID,
			DocPath:    group.DocPath,
			Field:      group.Field,
			AddRefs:    group.Refs,
			Evidence:   group.Evidence,
			Status:     MappingProposalStatusProposed,
		})
	}
	return proposals, nil
}

// proposalGroupKey is the (docPath, field) identity used to group suggestions.
type proposalGroupKey struct {
	docPath string
	field   MappingProposalField
}

// isMappingProposalField reports membership in MappingProposalFields.
func isMappingProposalField(field MappingProposalField) bool {
	for _, f := range MappingProposalFields {
		if MappingProposalField(f) == field {
			return true
		}
	}
	return false
}

// sortProposalGroups sorts by (docPath, field) ascending (string order).
func sortProposalGroups(groups []*proposalGroup) {
	for i := 1; i < len(groups); i++ {
		for j := i; j > 0 && proposalGroupLess(groups[j], groups[j-1]); j-- {
			groups[j], groups[j-1] = groups[j-1], groups[j]
		}
	}
}

func proposalGroupLess(a, b *proposalGroup) bool {
	if a.DocPath != b.DocPath {
		return a.DocPath < b.DocPath
	}
	return a.Field < b.Field
}

// joinComma joins strings with ", ".
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// itoaInt renders an int in base 10 (proposal suggestion indices).
func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
