// Ported from packages/policy/src/evaluation/harness.ts.

package policy

import "github.com/DUDE616/docSync-go/internal/schemas"

// HarnessOutcome is the per-case outcome vocabulary used for the agreement
// summary (`'updated' | 'abstained' | 'missed'`).
type HarnessOutcome string

// The per-case outcomes (HarnessCaseEvaluation.outcome).
const (
	HarnessOutcomeUpdated   HarnessOutcome = "updated"
	HarnessOutcomeAbstained HarnessOutcome = "abstained"
	HarnessOutcomeMissed    HarnessOutcome = "missed"
)

// HarnessCaseEvaluation is the minimal per-case evaluation shape used for the
// agreement summary (HarnessCaseEvaluation).
type HarnessCaseEvaluation struct {
	CaseID  string         `json:"caseId"`
	Outcome HarnessOutcome `json:"outcome"`
	// Correct reports whether the outcome was CORRECT (a true negative counts
	// as a correct abstain). A nil pointer means "unset" — the TS
	// `correct?: boolean` tri-state read as `correct === true`.
	Correct *bool `json:"correct,omitempty"`
}

// CaseEvaluations is the injected per-case replay payload (CaseEvaluations).
type CaseEvaluations struct {
	PolicyVersion int                     `json:"policyVersion"`
	Cases         []HarnessCaseEvaluation `json:"cases"`
}

// AggregateFunc is the injected aggregation — core metrics own the
// six-metric definitions (OfflineEvalHarnessDeps.aggregate).
type AggregateFunc func(evals CaseEvaluations) schemas.EvaluationReport

// OfflineEvalHarnessDeps are the harness dependencies
// (OfflineEvalHarnessDeps).
type OfflineEvalHarnessDeps struct {
	// Aggregate is the injected aggregation (core/src/evaluation/metrics.ts
	// owns the six-metric definitions — this leaf package mirrors nothing).
	Aggregate         AggregateFunc
	MinComparisonSize *int     `json:"minComparisonSize,omitempty"`
	Alpha             *float64 `json:"alpha,omitempty"`
}

// OfflineEvalHarnessInput is the per-run harness input
// (OfflineEvalHarnessInput).
type OfflineEvalHarnessInput struct {
	BaselineReport             schemas.EvaluationReport `json:"baselineReport"`
	BaselineCases              []HarnessCaseEvaluation  `json:"baselineCases"`
	CandidateCases             []HarnessCaseEvaluation  `json:"candidateCases"`
	CandidatePolicyVersion     int                      `json:"candidatePolicyVersion"`
	CandidateUnsafeAutoUpdates int                      `json:"candidateUnsafeAutoUpdates"`
	CandidateWins              int                      `json:"candidateWins"`
	CandidateTotal             int                      `json:"candidateTotal"`
	BaselineWins               int                      `json:"baselineWins"`
	BaselineTotal              int                      `json:"baselineTotal"`
	RepoMinComparisonSize      *int                     `json:"repoMinComparisonSize,omitempty"`
}

// ComparisonDeltas is the candidate-minus-baseline metric delta block
// (ComparisonReport.deltas).
type ComparisonDeltas struct {
	DetectionRecall   float64 `json:"detection_recall"`
	Precision         float64 `json:"precision"`
	PatchCorrectness  float64 `json:"patch_correctness"`
	FalseUpdateRate   float64 `json:"false_update_rate"`
	AbstentionQuality float64 `json:"abstention_quality"`
}

// AgreementSummary is the per-case agreement matrix between the baseline and
// the candidate replay (ComparisonReport.agreementSummary).
type AgreementSummary struct {
	BothCorrect          int `json:"bothCorrect"`
	BothWrong            int `json:"bothWrong"`
	CandidateOnlyCorrect int `json:"candidateOnlyCorrect"`
	BaselineOnlyCorrect  int `json:"baselineOnlyCorrect"`
}

// ComparisonVerdict is the harness verdict (`'promotable' | 'blocked'`).
type ComparisonVerdict string

// The harness verdicts.
const (
	VerdictPromotable ComparisonVerdict = "promotable"
	VerdictBlocked    ComparisonVerdict = "blocked"
)

// ComparisonReport is the emitted comparison report + gate checklist
// (ComparisonReport).
type ComparisonReport struct {
	BaselineReport   schemas.EvaluationReport `json:"baselineReport"`
	CandidateReport  schemas.EvaluationReport `json:"candidateReport"`
	Deltas           ComparisonDeltas         `json:"deltas"`
	AgreementSummary AgreementSummary         `json:"agreementSummary"`
	GateChecklist    PromotionGateResult      `json:"gateChecklist"`
	Verdict          ComparisonVerdict        `json:"verdict"`
}

// OfflineEvalHarness is the offline evaluation harness (OfflineEvalHarness).
type OfflineEvalHarness struct {
	gates *PromotionGateLibrary
	deps  OfflineEvalHarnessDeps
}

// NewOfflineEvalHarness creates the harness (createOfflineEvalHarness). The TS
// factory threw synchronously on malformed gate options; Go returns the coded
// error from the gate library constructor.
func NewOfflineEvalHarness(deps OfflineEvalHarnessDeps) (*OfflineEvalHarness, error) {
	gates, err := NewPromotionGateLibrary(PromotionGateLibraryOptions{
		MinComparisonSize: deps.MinComparisonSize,
		Alpha:             deps.Alpha,
	})
	if err != nil {
		return nil, err
	}
	return &OfflineEvalHarness{gates: gates, deps: deps}, nil
}

// Run replays the CANDIDATE cases through the INJECTED aggregation, computes
// the agreement summary against the baseline cases, and emits the comparison
// report + gate checklist (OfflineEvalHarness.run).
func (h *OfflineEvalHarness) Run(input OfflineEvalHarnessInput) (ComparisonReport, error) {
	candidateReport := h.deps.Aggregate(CaseEvaluations{
		PolicyVersion: input.CandidatePolicyVersion,
		Cases:         input.CandidateCases,
	})

	candidateByID := make(map[string]HarnessCaseEvaluation, len(input.CandidateCases))
	for _, candidateCase := range input.CandidateCases {
		candidateByID[candidateCase.CaseID] = candidateCase
	}

	var summary AgreementSummary
	for _, baselineCase := range input.BaselineCases {
		candidateCase, ok := candidateByID[baselineCase.CaseID]
		if !ok {
			continue
		}
		b := baselineCase.Correct != nil && *baselineCase.Correct
		c := candidateCase.Correct != nil && *candidateCase.Correct
		switch {
		case b && c:
			summary.BothCorrect++
		case !b && !c:
			summary.BothWrong++
		case c:
			summary.CandidateOnlyCorrect++
		default:
			summary.BaselineOnlyCorrect++
		}
	}

	gateChecklist, err := h.gates.CheckGates(PromotionGateInputs{
		Baseline:                   input.BaselineReport,
		Candidate:                  candidateReport,
		CandidateUnsafeAutoUpdates: input.CandidateUnsafeAutoUpdates,
		CandidateWins:              input.CandidateWins,
		CandidateTotal:             input.CandidateTotal,
		BaselineWins:               input.BaselineWins,
		BaselineTotal:              input.BaselineTotal,
		RepoMinComparisonSize:      input.RepoMinComparisonSize,
	})
	if err != nil {
		return ComparisonReport{}, err
	}

	verdict := VerdictBlocked
	if gateChecklist.Passed {
		verdict = VerdictPromotable
	}

	return ComparisonReport{
		BaselineReport:  input.BaselineReport,
		CandidateReport: candidateReport,
		Deltas: ComparisonDeltas{
			DetectionRecall:   candidateReport.Metrics.DetectionRecall - input.BaselineReport.Metrics.DetectionRecall,
			Precision:         candidateReport.Metrics.Precision - input.BaselineReport.Metrics.Precision,
			PatchCorrectness:  candidateReport.Metrics.PatchCorrectness - input.BaselineReport.Metrics.PatchCorrectness,
			FalseUpdateRate:   candidateReport.Metrics.FalseUpdateRate - input.BaselineReport.Metrics.FalseUpdateRate,
			AbstentionQuality: candidateReport.Metrics.AbstentionQuality - input.BaselineReport.Metrics.AbstentionQuality,
		},
		AgreementSummary: summary,
		GateChecklist:    gateChecklist,
		Verdict:          verdict,
	}, nil
}
