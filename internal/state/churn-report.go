package state

import "github.com/DUDE616/docSync-go/internal/schemas"

// Ported from core/src/state/churn-report.ts: Phase 0 consumability
// instrumentation (P0-T16). The D1 tree makes every state revisit observable
// — this aggregation answers "did the committed-`state/` revisit stay inside
// the CI contract?" from the collected run records, and the answer reviews
// threshold breaches instead of forcing a redesign when the data justifies it.

// ChurnReviewThreshold is the churn level that flips a verdict from flat
// "in contract" to "review" — 50 changed state files per observed repo run
// (not per commit).
const ChurnReviewThreshold = 50

// ChurnDataSource tags how a batch of run records was collected.
type ChurnDataSource string

// ChurnDataSource values.
const (
	ChurnDataSourceFixture ChurnDataSource = "fixture"
	ChurnDataSourcePilot   ChurnDataSource = "pilot"
)

// AggregateChurnInput is one aggregation input: the collected run records and
// how they were collected.
type AggregateChurnInput struct {
	Records    []schemas.RunRecord
	DataSource ChurnDataSource
}

// ChurnReport is the aggregation output. `totalStateReads` is RESERVED and
// stays absent until state-read instrumentation is defined — a derived
// placeholder would be fabricated data.
type ChurnReport struct {
	Runs             int             `json:"runs"`
	TotalStateWrites int             `json:"total_state_writes"`
	TotalStateReads  *int            `json:"total_state_reads,omitempty"`
	MeanWritesPerRun float64         `json:"mean_writes_per_run"`
	WorstRunRunID    *string         `json:"worst_run_run_id,omitempty"`
	DataSource       ChurnDataSource `json:"data_source"`
}

// AggregateChurn folds a batch of run records into the churn view: runs
// observed, total state writes, mean writes per run, and the run id with the
// most writes (first wins on ties).
func AggregateChurn(input AggregateChurnInput) ChurnReport {
	report := ChurnReport{
		Runs:       len(input.Records),
		DataSource: input.DataSource,
	}
	if report.DataSource == "" {
		report.DataSource = ChurnDataSourceFixture
	}
	worstWrites := -1
	for i := range input.Records {
		writes := input.Records[i].StateChurn.Added + input.Records[i].StateChurn.Updated
		report.TotalStateWrites += writes
		if writes > 0 && writes > worstWrites {
			worstWrites = writes
			runID := input.Records[i].RunID
			report.WorstRunRunID = &runID
		}
	}
	if report.Runs > 0 {
		report.MeanWritesPerRun = float64(report.TotalStateWrites) / float64(report.Runs)
	}
	return report
}
