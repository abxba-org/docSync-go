package contracts

import "github.com/DUDE616/docSync-go/internal/schemas"

// BootstrapContractKindConfidence is the bootstrap confidence per contract
// kind, keyed by the signal that hit. Bootstrap policy data (D11):
// recalibrating is a policy-version change, not a code change to the
// classification logic. Ported verbatim from classify.ts
// (BOOTSTRAP_CONTRACT_KIND_CONFIDENCE).
var BootstrapContractKindConfidence = map[schemas.ContractKind]float64{
	schemas.ContractKindAPISchema: 0.98,
	schemas.ContractKindConfig:    0.9,
	schemas.ContractKindBehavior:  0.85,
	schemas.ContractKindContent:   0.8,
}

// DefaultContractReviewThreshold is the conservative default review threshold:
// the weakest bootstrap signal (content, 0.8) abstains to REVIEW by default,
// while explicit stronger signals (api_schema/config/behavior) classify
// confidently. Sub-threshold ⇒ abstain: true — routed REVIEW, never auto-scoped.
const DefaultContractReviewThreshold = 0.85

// OpenApiTier is the three-tier OpenAPI delta taxonomy. SEAM: mirrors
// internal/analysis OpenApiTier (consumed read-only by the classifier).
type OpenApiTier string

const (
	OpenApiTierBreaking           OpenApiTier = "breaking"
	OpenApiTierBehaviorAnnotating OpenApiTier = "behavior-annotating"
	OpenApiTierNonBreaking        OpenApiTier = "non-breaking"
)

// ApiDeltaSummary is the aggregate tier counts of an OpenAPI delta
// classification. SEAM: mirrors internal/analysis OpenApiDeltaSummary (only the
// fields the classifier reads).
type ApiDeltaSummary struct {
	Breaking           int         `json:"breaking"`
	BehaviorAnnotating int         `json:"behaviorAnnotating"`
	NonBreaking        int         `json:"nonBreaking"`
	WorstTier          OpenApiTier `json:"worstTier"`
}

// ApiDeltaEntry is one classified OpenAPI delta. SEAM: mirrors internal/analysis
// OpenApiDeltaEntry (only the location is read here).
type ApiDeltaEntry struct {
	Location string `json:"location"`
}

// ApiDelta is the run's classified OpenAPI delta (P11-T10 level-4 evidence).
// SEAM: mirrors internal/analysis OpenApiDeltaClassification. A BREAKING delta
// rouses contract rows on docs mapped via `api` edges; non-breaking deltas
// never rouse (conservative — abstain, don't guess).
type ApiDelta struct {
	Deltas       []ApiDeltaEntry `json:"deltas"`
	Summary      ApiDeltaSummary `json:"summary"`
	RulesVersion string          `json:"rulesVersion"`
}

// ConfigKeyDiffChange classifies one config-key diff. SEAM: mirrors
// internal/analysis ConfigKeyDiffChange.
type ConfigKeyDiffChange string

const (
	ConfigKeyAdded   ConfigKeyDiffChange = "added"
	ConfigKeyRemoved ConfigKeyDiffChange = "removed"
	ConfigKeyChanged ConfigKeyDiffChange = "changed"
)

// ConfigKeyDiff is one key-level resolved-value diff entry. SEAM: mirrors
// internal/analysis ConfigKeyDiff (only key + change are read here). ANY key
// change rouses contract rows on docs mapped via `config` edges.
type ConfigKeyDiff struct {
	Key    string              `json:"key"`
	Change ConfigKeyDiffChange `json:"change"`
}

// ContractApiDeltaEvidence is the delta classification attached to an
// api-edge-roused contract row (P11-T10).
type ContractApiDeltaEvidence struct {
	WorstTier          OpenApiTier `json:"worstTier"`
	Breaking           int         `json:"breaking"`
	BehaviorAnnotating int         `json:"behaviorAnnotating"`
	NonBreaking        int         `json:"nonBreaking"`
	// Verbatim classifier delta locations (JSON-pointer style).
	Locations []string `json:"locations"`
}

// ContractVectorEntry is one contract's classification vector entry.
type ContractVectorEntry struct {
	Contract   string               `json:"contract"`
	Kind       schemas.ContractKind `json:"kind"`
	Hit        bool                 `json:"hit"`
	Confidence float64              `json:"confidence"`
	// Deterministic signal evidence, e.g. `src/api/schema.ts (api_diff)`.
	Evidence []string `json:"evidence"`
	// True when confidence < threshold — routes REVIEW, never auto-scoped.
	Abstain bool `json:"abstain"`
	// SymbolTypeChanges: symbol-level `type`-change evidence on files mapped to
	// this doc via its code (sources) edges — `file#symbol` entries. Present only
	// when non-empty (nil = no symbol evidence).
	SymbolTypeChanges []string `json:"symbolTypeChanges,omitempty"`
	// ApiDeltaEvidence: the BREAKING OpenAPI delta classification attached through
	// the doc's `api` edges. Present only when attached (nil = none).
	ApiDeltaEvidence *ContractApiDeltaEvidence `json:"apiDeltaEvidence,omitempty"`
	// ConfigKeyEvidence: key-level config-diff evidence attached through the
	// doc's `config` edges — `key (change)` entries. Present only when non-empty
	// (nil = no config evidence).
	ConfigKeyEvidence []string `json:"configKeyEvidence,omitempty"`
}

// ContractVector is the per-document contract vector.
type ContractVector struct {
	Doc     string                `json:"doc"`
	Vectors []ContractVectorEntry `json:"vectors"`
}

// ClassifyContractsInput is the classifier input (P11-T10 additive optional
// evidence: apiDelta, configDiff).
type ClassifyContractsInput struct {
	Graph          *Graph
	Doc            string
	ChangeAnalysis *schemas.ChangeAnalysis
	// Review threshold; zero value uses DefaultContractReviewThreshold.
	Threshold float64
	// ApiDelta is the run's classified OpenAPI delta (level-4 evidence). nil = none.
	ApiDelta *ApiDelta
	// ConfigDiff is the run's key-level config diffs (level-4 evidence). nil = none.
	ConfigDiff []ConfigKeyDiff
}

// evidenceForKind collects signal evidence for one contract kind over the
// changed files (declaration/file order preserved).
func evidenceForKind(kind schemas.ContractKind, files []schemas.ChangedFile) []string {
	evidence := []string{}
	for i := range files {
		file := &files[i]
		switch kind {
		case schemas.ContractKindAPISchema:
			if file.APIDiff != nil && *file.APIDiff {
				evidence = append(evidence, file.Path+" (api_diff)")
			}
		case schemas.ContractKindConfig:
			if file.ConfigDiff != nil && *file.ConfigDiff {
				evidence = append(evidence, file.Path+" (config_diff)")
			}
		case schemas.ContractKindBehavior:
			if file.SymbolsChanged != nil && *file.SymbolsChanged {
				evidence = append(evidence, file.Path+" (symbols_changed)")
			}
			if file.TestOnly != nil && *file.TestOnly {
				evidence = append(evidence, file.Path+" (test_only)")
			}
		case schemas.ContractKindContent:
			// "Any non-formatting-only change": formatting-only files carry no
			// content signal; every other changed file does.
			if file.FormattingOnly == nil || !*file.FormattingOnly {
				evidence = append(evidence, file.Path+" (non-formatting)")
			}
		}
	}
	return evidence
}

// symbolTypeEvidence collects `type`-kind symbol changes on files mapped to the
// doc via its code (sources) edges — `file#symbol (type change)` entries, file
// order, symbols_detail order within a file, first-occurrence dedupe.
func symbolTypeEvidence(mapping *MappingRecord, files []schemas.ChangedFile) ([]string, error) {
	out := []string{}
	for i := range files {
		file := &files[i]
		if file.FormattingOnly != nil && *file.FormattingOnly {
			continue
		}
		if file.SymbolsDetail == nil {
			continue
		}
		matched := false
		for _, source := range mapping.Sources {
			ok, err := SourceMatchesPath(source, file.Path)
			if err != nil {
				return nil, err
			}
			if ok {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, change := range file.SymbolsDetail.Changed {
			if !changeKindsInclude(change.ChangeKinds, schemas.SymbolChangeType) {
				continue
			}
			entry := file.Path + "#" + change.Name + " (type change)"
			if !containsString(out, entry) {
				out = append(out, entry)
			}
		}
	}
	return out, nil
}

// apiDeltaEvidenceFor returns the BREAKING OpenAPI delta attached through the
// doc's `api` edges. Only a delta with ≥1 breaking entry rouses (conservative);
// the full classification rides the row. nil = not attached.
func apiDeltaEvidenceFor(mapping *MappingRecord, apiDelta *ApiDelta) *ContractApiDeltaEvidence {
	if apiDelta == nil || len(mapping.Apis) == 0 {
		return nil
	}
	if apiDelta.Summary.Breaking == 0 {
		return nil
	}
	locations := make([]string, 0, len(apiDelta.Deltas))
	for _, delta := range apiDelta.Deltas {
		locations = append(locations, delta.Location)
	}
	return &ContractApiDeltaEvidence{
		WorstTier:          apiDelta.Summary.WorstTier,
		Breaking:           apiDelta.Summary.Breaking,
		BehaviorAnnotating: apiDelta.Summary.BehaviorAnnotating,
		NonBreaking:        apiDelta.Summary.NonBreaking,
		Locations:          locations,
	}
}

// configKeyEvidenceFor returns key-level config diffs attached through the doc's
// `config` edges — ANY key change rouses; entries are `key (change)` in diff
// order. nil = not attached.
func configKeyEvidenceFor(mapping *MappingRecord, configDiff []ConfigKeyDiff) []string {
	if configDiff == nil || len(configDiff) == 0 {
		return nil
	}
	if len(mapping.Configs) == 0 {
		return nil
	}
	out := make([]string, 0, len(configDiff))
	for _, diff := range configDiff {
		out = append(out, diff.Key+" ("+string(diff.Change)+")")
	}
	return out
}

// ClassifyContracts classifies every contract declared on doc against the
// ChangeAnalysis. Deterministic: declaration order (mapping.contracts), files in
// ChangeAnalysis order. A doc without a mapping (or without declared contracts)
// yields an empty Vectors slice — an unmapped doc is invisible to the graph (D7
// handles unknown coverage manifest-side).
//
// An error is returned only to propagate a hard glob-guard failure while
// scanning a doc's sources (fail closed, D8) — exactly where the TS compileGlob
// inside symbolTypeEvidence would have thrown.
func ClassifyContracts(input ClassifyContractsInput) (ContractVector, error) {
	threshold := input.Threshold
	if threshold == 0 {
		threshold = DefaultContractReviewThreshold
	}
	vector := ContractVector{Doc: input.Doc, Vectors: []ContractVectorEntry{}}
	mapping := ForwardLookup(input.Graph, input.Doc)
	if mapping == nil {
		return vector, nil
	}
	files := input.ChangeAnalysis.Files
	symbolTypeChanges, err := symbolTypeEvidence(mapping, files)
	if err != nil {
		return ContractVector{}, err
	}
	apiDeltaEvidence := apiDeltaEvidenceFor(mapping, input.ApiDelta)
	configKeyEvidence := configKeyEvidenceFor(mapping, input.ConfigDiff)
	for _, contract := range mapping.Contracts {
		kind, ok := input.Graph.ContractKinds[contract]
		if !ok {
			kind = schemas.ContractKindContent
		}
		evidence := evidenceForKind(kind, files)
		// The new diff kinds rouse the row (hit) additively — the kind-level
		// vocabulary and confidence policy are unchanged.
		roused := len(evidence) > 0 ||
			len(symbolTypeChanges) > 0 ||
			apiDeltaEvidence != nil ||
			len(configKeyEvidence) > 0
		confidence := 0.0
		if roused {
			confidence = BootstrapContractKindConfidence[kind]
		}
		entry := ContractVectorEntry{
			Contract:   contract,
			Kind:       kind,
			Hit:        roused,
			Confidence: confidence,
			Evidence:   evidence,
			Abstain:    confidence < threshold,
		}
		if len(symbolTypeChanges) > 0 {
			entry.SymbolTypeChanges = symbolTypeChanges
		}
		if apiDeltaEvidence != nil {
			entry.ApiDeltaEvidence = apiDeltaEvidence
		}
		if len(configKeyEvidence) > 0 {
			entry.ConfigKeyEvidence = configKeyEvidence
		}
		vector.Vectors = append(vector.Vectors, entry)
	}
	return vector, nil
}

// changeKindsInclude reports whether kinds contains want.
func changeKindsInclude(kinds []schemas.SymbolChangeKind, want schemas.SymbolChangeKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// containsString reports whether s is in list.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
