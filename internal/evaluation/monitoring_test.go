// Ported from core/src/../../test/steady-state-monitoring-p12-t19.test.ts
// (P12-T19; ARCH §5.2, §9, §10.6; R11, R13).
//
// Covers the ONE deterministic steady-state surface: per-policy-version cost
// attribution grouped by (policy_version, model binding), the abstain-health
// monitor with a CALLER-SUPPLIED band (D11 — never hard-coded), the R13
// spot-check selection-bias guard, the ARCH §8.6 unsafe-auto-updates gate
// floor, the INV-11-capped advisory dashboard renderer, and determinism (P19:
// no hidden clock — same input ⇒ deep-equal output).
package evaluation_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/evaluation"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func f(v float64) *float64 { return &v }

// usage builds one cost-attribution usage row.
func usageRow(role, model string, promptTokens, completionTokens int, costUsd *float64) evaluation.CostAttributionUsage {
	return evaluation.CostAttributionUsage{
		Role:             role,
		Model:            model,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CostUsd:          costUsd,
	}
}

// makeRun builds one steady-state run contribution with the TS fixture
// defaults, applying the given mutation.
func makeRun(runID string, policyVersion int, mut func(*evaluation.SteadyStateRunInput)) evaluation.SteadyStateRunInput {
	run := evaluation.SteadyStateRunInput{
		RunID:               runID,
		Repo:                repo,
		PolicyVersion:       policyVersion,
		Decisions:           100,
		Abstains:            20,
		Overrides:           10,
		AutoApplied:         40,
		SpotCheckSampled:    4,
		SpotCheckChecked:    4,
		AuditMispredictions: 0,
		FalsePositives:      5,
		FalseNegatives:      3,
		UnsafeAutoUpdates:   0,
	}
	if mut != nil {
		mut(&run)
	}
	return run
}

func makeRow(t *testing.T, repoName string, policyVersion int, runs []evaluation.SteadyStateRunInput) *evaluation.SteadyStateRow {
	t.Helper()
	row, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
		Repo:          repoName,
		PolicyVersion: policyVersion,
		Runs:          runs,
	})
	require.NoError(t, err)
	return row
}

// Two policy versions, mixed bindings (writer model changes across versions).
func attributionRuns() []evaluation.SteadyStateRunInput {
	return []evaluation.SteadyStateRunInput{
		makeRun("run_17a", 17, func(r *evaluation.SteadyStateRunInput) {
			r.Usages = []evaluation.CostAttributionUsage{
				usageRow("writer", "anthropic/claude-sonnet-4.5", 1000, 200, f(0.05)),
				usageRow("decision-engine", "z-ai/glm-4.7", 500, 100, f(0.01)),
			}
		}),
		makeRun("run_17b", 17, func(r *evaluation.SteadyStateRunInput) {
			r.Usages = []evaluation.CostAttributionUsage{
				usageRow("writer", "anthropic/claude-sonnet-4.5", 800, 100, f(0.03)),
				usageRow("judge", "z-ai/glm-4.7", 300, 50, f(0.005)),
			}
		}),
		makeRun("run_18a", 18, func(r *evaluation.SteadyStateRunInput) {
			r.Usages = []evaluation.CostAttributionUsage{
				usageRow("writer", "anthropic/claude-sonnet-4.6", 1200, 300, f(0.07)),
				usageRow("decision-engine", "z-ai/glm-4.7", 600, 90, f(0.012)),
			}
		}),
	}
}

func TestSteadyStateCostAttribution(t *testing.T) {
	t.Run("groups run records by (policy_version, model binding) and sums usage", func(t *testing.T) {
		runs := attributionRuns()
		groups, err := evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(runs))
		require.NoError(t, err)
		type key struct {
			policyVersion int
			role          string
			model         string
		}
		got := make([]key, 0, len(groups))
		for _, group := range groups {
			got = append(got, key{group.PolicyVersion, group.Role, group.Model})
		}
		assert.Equal(t, []key{
			{17, "decision-engine", "z-ai/glm-4.7"},
			{17, "judge", "z-ai/glm-4.7"},
			{17, "writer", "anthropic/claude-sonnet-4.5"},
			{18, "decision-engine", "z-ai/glm-4.7"},
			{18, "writer", "anthropic/claude-sonnet-4.6"},
		}, got)
		var writer17 *evaluation.CostAttributionGroup
		for i := range groups {
			if groups[i].PolicyVersion == 17 && groups[i].Role == "writer" {
				writer17 = &groups[i]
			}
		}
		require.NotNil(t, writer17)
		assert.Equal(t, 2, writer17.UsageCount)
		assert.Equal(t, 1800, writer17.PromptTokens)
		assert.Equal(t, 300, writer17.CompletionTokens)
		assert.Equal(t, 2100, writer17.TotalTokens)
		assert.InDelta(t, 0.08, writer17.TotalCostUsd, 1e-12)
		assert.Equal(t, []string{"run_17a", "run_17b"}, writer17.RunIDs)
		assert.Equal(t, 0, writer17.UsagesWithoutCost)
	})

	t.Run("a binding change is a NEW attribution group (confidence stays pinned to policy_version + binding, D3)", func(t *testing.T) {
		groups, err := evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(attributionRuns()))
		require.NoError(t, err)
		var writers []evaluation.CostAttributionGroup
		for _, group := range groups {
			if group.Role == "writer" {
				writers = append(writers, group)
			}
		}
		require.Len(t, writers, 2)
		assert.Equal(t, "anthropic/claude-sonnet-4.5", writers[0].Model)
		assert.Equal(t, 17, writers[0].PolicyVersion)
		assert.Equal(t, "anthropic/claude-sonnet-4.6", writers[1].Model)
		assert.Equal(t, 18, writers[1].PolicyVersion)
	})

	t.Run("row attribution carries only the groups of that row policy version", func(t *testing.T) {
		runs := attributionRuns()
		row17 := makeRow(t, repo, 17, runs[:2])
		row18 := makeRow(t, repo, 18, runs[2:])
		versions17 := make([]int, 0, len(row17.CostAttribution))
		for _, group := range row17.CostAttribution {
			versions17 = append(versions17, group.PolicyVersion)
		}
		assert.Equal(t, []int{17, 17, 17}, versions17)
		versions18 := make([]int, 0, len(row18.CostAttribution))
		for _, group := range row18.CostAttribution {
			versions18 = append(versions18, group.PolicyVersion)
		}
		assert.Equal(t, []int{18, 18}, versions18)
		assert.Len(t, row17.CostAttribution, 3)
	})

	t.Run("an unknown gateway cost is surfaced as unknown, never guessed to zero (ARCH §10.6)", func(t *testing.T) {
		groups, err := evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(
			[]evaluation.SteadyStateRunInput{
				makeRun("run_nocost", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Usages = []evaluation.CostAttributionUsage{
						usageRow("writer", "anthropic/claude-sonnet-4.5", 100, 20, nil),
					}
				}),
			}))
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, 1, groups[0].UsagesWithoutCost)
		assert.Equal(t, 0.0, groups[0].TotalCostUsd)
		// Known rows are still counted when the gateway omitted cost on others.
		mixed, err := evaluation.CostAttributionByPolicyVersion(evaluation.CostAttributionRunsFrom(
			[]evaluation.SteadyStateRunInput{
				makeRun("run_mixed", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Usages = []evaluation.CostAttributionUsage{
						usageRow("writer", "anthropic/claude-sonnet-4.5", 100, 20, f(0.05)),
					}
				}),
				makeRun("run_mixed_2", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Usages = []evaluation.CostAttributionUsage{
						usageRow("writer", "anthropic/claude-sonnet-4.5", 10, 5, nil),
					}
				}),
			}))
		require.NoError(t, err)
		require.Len(t, mixed, 1)
		assert.Equal(t, 1, mixed[0].UsagesWithoutCost)
		assert.InDelta(t, 0.05, mixed[0].TotalCostUsd, 1e-12)
		assert.Equal(t, 2, mixed[0].UsageCount)
	})

	t.Run("the summary merges per-row attributions onto the (policy_version, binding) key", func(t *testing.T) {
		runs := attributionRuns()
		row17 := makeRow(t, repo, 17, runs[:2])
		row18 := makeRow(t, repo, 18, runs[2:])
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row17, row18},
		})
		require.NoError(t, err)
		assert.Len(t, summary.CostAttribution, 5)
		var judge *evaluation.CostAttributionGroup
		for i := range summary.CostAttribution {
			if summary.CostAttribution[i].Role == "judge" {
				judge = &summary.CostAttribution[i]
			}
		}
		require.NotNil(t, judge)
		assert.Equal(t, 17, judge.PolicyVersion)
		assert.Equal(t, []string{"run_17b"}, judge.RunIDs)
		assert.Equal(t, 6, summary.Totals.UsageRecords)
		assert.Equal(t, 600+350+2100+690+1500, summary.Totals.TotalTokens)
		assert.InDelta(t, 0.177, summary.Totals.TotalCostUsd, 1e-12)
	})
}

func TestSteadyStateAbstainHealthMonitor(t *testing.T) {
	// A row whose abstain rate (0.60) sits above a [0.10, 0.30] band.
	outOfBandRow := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
		makeRun("run_gap", 17, func(r *evaluation.SteadyStateRunInput) {
			r.Abstains = 60
		}),
	})
	// A row whose abstain rate (0.20) sits inside a [0.10, 0.30] band.
	inBandRow := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
		makeRun("run_ok", 17, nil),
	})

	t.Run("flags an out-of-band row and passes an in-band row against the caller-supplied band", func(t *testing.T) {
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{outOfBandRow, inBandRow},
			AbstainHealthBand: &evaluation.AbstainHealthBand{
				MinAbstainRate: f(0.1),
				MaxAbstainRate: f(0.3),
			},
		})
		require.NoError(t, err)
		assert.Equal(t, []evaluation.AbstainHealthDriftFlag{
			{
				Key:           evaluation.SteadyStateRowKey(repo, 17),
				Repo:          repo,
				PolicyVersion: 17,
				AbstainRate:   0.6,
				Direction:     evaluation.DriftDirectionAboveMax,
			},
		}, summary.AbstainHealth.DriftFlags)
		assert.False(t, summary.AbstainHealth.InBand)
		require.NotNil(t, summary.AbstainHealth.Band)
		assert.Equal(t, 0.1, *summary.AbstainHealth.Band.MinAbstainRate)
		assert.Equal(t, 0.3, *summary.AbstainHealth.Band.MaxAbstainRate)
	})

	t.Run("the band is DATA: a different caller-supplied band moves the flag (never hard-coded, D11)", func(t *testing.T) {
		rows := []*evaluation.SteadyStateRow{outOfBandRow, inBandRow}
		strict, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: rows,
			AbstainHealthBand: &evaluation.AbstainHealthBand{
				MinAbstainRate: f(0.55),
				MaxAbstainRate: f(0.9),
			},
		})
		require.NoError(t, err)
		// The 0.60-abstain row is now IN band; the 0.20-abstain row is now below min.
		assert.Equal(t, []evaluation.AbstainHealthDriftFlag{
			{
				Key:           evaluation.SteadyStateRowKey(repo, 17),
				Repo:          repo,
				PolicyVersion: 17,
				AbstainRate:   0.2,
				Direction:     evaluation.DriftDirectionBelowMin,
			},
		}, strict.AbstainHealth.DriftFlags)

		unbanded, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{Rows: rows})
		require.NoError(t, err)
		assert.Nil(t, unbanded.AbstainHealth.Band)
		assert.Equal(t, []evaluation.AbstainHealthDriftFlag{}, unbanded.AbstainHealth.DriftFlags)
		assert.True(t, unbanded.AbstainHealth.InBand)
	})

	t.Run("an empty band is refused (fail closed — a silent no-op band is not a monitor)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows:              []*evaluation.SteadyStateRow{inBandRow},
			AbstainHealthBand: &evaluation.AbstainHealthBand{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must supply at least one of minAbstainRate / maxAbstainRate")
		_, err = evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{inBandRow},
			AbstainHealthBand: &evaluation.AbstainHealthBand{
				MinAbstainRate: f(0.8),
				MaxAbstainRate: f(0.2),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "min (0.8) exceeds max (0.2)")
	})

	t.Run("surfaces the Phase-10 2-D alerts (policy_gap / overconfidence) reused from the metrics module", func(t *testing.T) {
		overAbstain := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
			makeRun("run_over", 17, func(r *evaluation.SteadyStateRunInput) {
				r.Abstains = 60
				r.Overrides = 40
			}),
		})
		underAbstain := makeRow(t, repo, 18, []evaluation.SteadyStateRunInput{
			makeRun("run_under", 18, func(r *evaluation.SteadyStateRunInput) {
				r.Abstains = 2
				r.Overrides = 50
			}),
		})
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{overAbstain, underAbstain},
		})
		require.NoError(t, err)
		assert.Equal(t, []evaluation.AbstainHealthAlertEntry{
			{Key: repo + "@v17", Repo: repo, PolicyVersion: 17, Alert: evaluation.AbstainHealthPolicyGap},
			{Key: repo + "@v18", Repo: repo, PolicyVersion: 18, Alert: evaluation.AbstainHealthOverconfidence},
		}, summary.AbstainHealth.Alerts)
	})
}

func TestSteadyStateSpotCheckCoverage(t *testing.T) {
	t.Run("reports the sampled rate and flags a zero-spot-check row (selection-bias guard)", func(t *testing.T) {
		sampled := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{makeRun("run_sampled", 17, nil)})
		noSample := makeRow(t, repo, 18, []evaluation.SteadyStateRunInput{
			makeRun("run_nosample", 18, func(r *evaluation.SteadyStateRunInput) {
				r.AutoApplied = 20
				r.SpotCheckSampled = 0
				r.SpotCheckChecked = 0
			}),
		})
		noAudit := makeRow(t, repo, 19, []evaluation.SteadyStateRunInput{
			makeRun("run_noaudit", 19, func(r *evaluation.SteadyStateRunInput) {
				r.AutoApplied = 10
				r.SpotCheckSampled = 1
				r.SpotCheckChecked = 0
			}),
		})
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{sampled, noSample, noAudit},
		})
		require.NoError(t, err)
		assert.InDelta(t, 5.0/70.0, summary.SpotCheckCoverage.SampledShareOfAutoApplied, 1e-12)
		assert.InDelta(t, 4.0/5.0, summary.SpotCheckCoverage.CoverageOfSample, 1e-12)
		assert.Equal(t, []evaluation.SpotCheckCoverageGap{
			{Key: repo + "@v18", Repo: repo, PolicyVersion: 18, Reason: evaluation.SpotCheckGapNoSample},
			{Key: repo + "@v19", Repo: repo, PolicyVersion: 19, Reason: evaluation.SpotCheckGapNoAudit},
		}, summary.SpotCheckCoverage.Gaps)
		assert.False(t, summary.SpotCheckCoverage.Complete)
	})

	t.Run("reuses the Phase-10 sampler expectation (SpotCheckSampleSize) for the OD-R25 rate", func(t *testing.T) {
		row := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
			makeRun("run_rate", 17, func(r *evaluation.SteadyStateRunInput) {
				r.AutoApplied = 40
			}),
		})
		expected, err := evaluation.SpotCheckSampleSize(40)
		require.NoError(t, err)
		assert.Equal(t, expected, row.SpotCheck.ExpectedSampleSize)
		assert.Equal(t, 4, row.SpotCheck.ExpectedSampleSize)
		assert.InDelta(t, 0.1, row.SpotCheck.SampledRate, 1e-12)
		assert.Equal(t, 1.0, row.SpotCheck.CheckedRate)
		assert.True(t, row.SpotCheck.SampleFloorMet)
	})

	t.Run("counts mispredictions from the ingested audit-finding label stream (reuse, never a side channel)", func(t *testing.T) {
		labels, err := evaluation.IngestAuditFindings([]evaluation.AuditFinding{
			{DecisionID: "d_01", PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchCorrect, At: "2026-10-08T00:00:00Z"},
			{DecisionID: "d_02", PolicyVersion: 17, Verdict: evaluation.AuditVerdictPatchIncorrect, At: "2026-10-08T00:00:01Z"},
			{DecisionID: "d_03", PolicyVersion: 17, Verdict: evaluation.AuditVerdictUpdateUnwarranted, At: "2026-10-08T00:00:02Z"},
		}, &evaluation.IngestAuditFindingsOptions{
			NowMs:     int64Ptr(0),
			RandBytes: constRng,
		})
		require.NoError(t, err)
		assert.Equal(t, 2, evaluation.CountAuditMispredictions(labels))
		// Non-audit labels never count as mispredictions (the stream is the
		// SAME one the evaluation loop reads).
		other := append([]schemas.HygieneLabel{}, labels...)
		for i := range other {
			other[i].SourceSignal = schemas.HygieneSourceSpotCheck
		}
		assert.Equal(t, 0, evaluation.CountAuditMispredictions(other))
	})
}

func int64Ptr(v int64) *int64 { return &v }

func TestSteadyStateUnsafeAutoUpdatesGate(t *testing.T) {
	runs := attributionRuns()
	row17 := makeRow(t, repo, 17, runs[:2])
	row18 := makeRow(t, repo, 18, runs[2:])

	t.Run("unsafe_auto_updates >= 1 makes the summary unhealthy and names the offending rows", func(t *testing.T) {
		unsafe := makeRow(t, repo, 18, []evaluation.SteadyStateRunInput{
			makeRun("run_unsafe", 18, func(r *evaluation.SteadyStateRunInput) {
				r.UnsafeAutoUpdates = 1
			}),
			makeRun("run_unsafe_2", 18, func(r *evaluation.SteadyStateRunInput) {
				r.UnsafeAutoUpdates = 2
			}),
		})
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row17, unsafe},
		})
		require.NoError(t, err)
		assert.Equal(t, 3, summary.UnsafeAutoUpdatesTotal)
		assert.False(t, summary.Healthy)
		assert.Equal(t, []string{repo + "@v18"}, summary.UnsafeRows)
		assert.Equal(t, []string{evaluation.SteadyStateRowFlagUnsafeAutoUpdates}, unsafe.Flags)
		assert.Equal(t, 3, unsafe.UnsafeAutoUpdates)
	})

	t.Run("a zero unsafe-auto-update window is healthy", func(t *testing.T) {
		inBandRow := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{makeRun("run_ok", 17, nil)})
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row17, row18, inBandRow},
		})
		require.NoError(t, err)
		assert.Equal(t, 0, summary.UnsafeAutoUpdatesTotal)
		assert.True(t, summary.Healthy)
		assert.Equal(t, []string{}, summary.UnsafeRows)
		assert.Equal(t, []string{}, inBandRow.Flags)
	})
}

func TestSteadyStateDashboardRenderer(t *testing.T) {
	runs := attributionRuns()
	row17 := makeRow(t, repo, 17, runs[:2])
	outOfBandRow := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
		makeRun("run_gap", 17, func(r *evaluation.SteadyStateRunInput) {
			r.Abstains = 60
		}),
	})

	t.Run("caps the tables at the INV-11 constant with an explicit overflow line", func(t *testing.T) {
		var rows []*evaluation.SteadyStateRow
		for index := 0; index < evaluation.SteadyStateRenderCap+3; index++ {
			repoName := "acme/repo-" + pad3(index)
			rows = append(rows, makeRow(t, repoName, 17, []evaluation.SteadyStateRunInput{
				makeRun("run_"+pad3(index), 17, func(r *evaluation.SteadyStateRunInput) {
					r.Repo = repoName
				}),
			}))
		}
		rows = append(rows, outOfBandRow)
		markdown := evaluation.RenderSteadyStateDashboard(rows, nil)
		assert.Equal(t, 50, evaluation.SteadyStateRenderCap)
		assert.Contains(t, markdown, "flood cap 50")
		// rows.length (cap + 3 + 1) − cap omitted rows.
		assert.Contains(t, markdown, strconv.Itoa(len(rows)-evaluation.SteadyStateRenderCap)+" more row(s) omitted")
		assert.Contains(t, markdown, "4 more row(s) omitted")
		// The capped-out repo never renders; the cap is never a silent truncation.
		assert.NotContains(t, markdown, "acme/repo-52")
		assert.Contains(t, markdown, "acme/repo-00")
	})

	t.Run("carries the advisory banner and a provenance footer, and implies no writes", func(t *testing.T) {
		markdown := evaluation.RenderSteadyStateDashboard([]*evaluation.SteadyStateRow{row17, outOfBandRow}, nil)
		assert.Contains(t, markdown, "ADVISORY")
		assert.Contains(t, markdown, "No document was modified")
		assert.Contains(t, markdown, "_Provenance: deterministic render of 2 steady-state row(s)")
		assert.Contains(t, markdown, "P12-T19")
		assert.Contains(t, markdown, "INV-11")
		// Advisory text only — no write instruction anywhere in the payload.
		lowered := strings.ToLower(markdown)
		for _, forbidden := range []string{
			"patch applied",
			"auto-apply",
			"auto-applied",
			"git push",
			"opened a pr",
			"docsync/fix",
			"commit the change",
		} {
			assert.NotContains(t, lowered, forbidden)
		}
	})

	t.Run("renders the abstain band, spot-check coverage and unsafe-gate monitor lines", func(t *testing.T) {
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row17, outOfBandRow},
			AbstainHealthBand: &evaluation.AbstainHealthBand{
				MinAbstainRate: f(0.1),
				MaxAbstainRate: f(0.3),
			},
		})
		require.NoError(t, err)
		markdown := evaluation.RenderSteadyStateDashboard(
			[]*evaluation.SteadyStateRow{row17, outOfBandRow},
			&evaluation.AbstainHealthBand{MinAbstainRate: f(0.1), MaxAbstainRate: f(0.3)})
		assert.Contains(t, markdown, "Abstain-health (R11)")
		assert.Contains(t, markdown, "Spot-check coverage (R13")
		assert.Contains(t, markdown, "Unsafe auto-updates (ARCH §8.6 gate floor)")
		assert.Contains(t, markdown, "[0.1, 0.3]")
		assert.Contains(t, markdown, "1 drift flag(s)")
		assert.True(t, summary.Healthy)
		// Without a band the renderer says so instead of inventing one (D11).
		assert.Contains(t, evaluation.RenderSteadyStateDashboard(
			[]*evaluation.SteadyStateRow{row17, outOfBandRow}, nil), "band not configured")
		breached := evaluation.RenderSteadyStateDashboard([]*evaluation.SteadyStateRow{
			makeRow(t, repo, 18, []evaluation.SteadyStateRunInput{
				makeRun("run_x", 18, func(r *evaluation.SteadyStateRunInput) {
					r.UnsafeAutoUpdates = 1
				}),
			}),
		}, nil)
		assert.Contains(t, breached, "GATE FLOOR BREACHED")
	})
}

func TestSteadyStateDeterminism(t *testing.T) {
	runs := attributionRuns()
	rows := []*evaluation.SteadyStateRow{
		makeRow(t, repo, 17, runs[:2]),
		makeRow(t, repo, 18, runs[2:]),
		makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
			makeRun("run_gap", 17, func(r *evaluation.SteadyStateRunInput) {
				r.Abstains = 60
			}),
		}),
		makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{makeRun("run_ok", 17, nil)}),
		makeRow(t, repo, 18, []evaluation.SteadyStateRunInput{
			makeRun("run_b", 18, func(r *evaluation.SteadyStateRunInput) {
				r.UnsafeAutoUpdates = 1
			}),
		}),
	}
	band := &evaluation.AbstainHealthBand{MinAbstainRate: f(0.1), MaxAbstainRate: f(0.5)}

	t.Run("same input ⇒ deep-equal summary (no clock, no randomness)", func(t *testing.T) {
		first, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: rows, AbstainHealthBand: band,
		})
		require.NoError(t, err)
		second, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: rows, AbstainHealthBand: band,
		})
		require.NoError(t, err)
		assert.Equal(t, first, second)
	})

	t.Run("row and attribution output is order-independent", func(t *testing.T) {
		reordered := make([]*evaluation.SteadyStateRow, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			reordered = append(reordered, rows[i])
		}
		forward, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: rows, AbstainHealthBand: band,
		})
		require.NoError(t, err)
		backward, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: reordered, AbstainHealthBand: band,
		})
		require.NoError(t, err)
		assert.Equal(t, forward, backward)
	})

	t.Run("the dashboard render is byte-identical for the same rows", func(t *testing.T) {
		first := evaluation.RenderSteadyStateDashboard(rows, nil)
		second := evaluation.RenderSteadyStateDashboard(append([]*evaluation.SteadyStateRow{}, rows...), nil)
		assert.Equal(t, first, second)
	})
}

func TestSteadyStateFailClosed(t *testing.T) {
	t.Run("refuses an empty window (an empty row has no evidence)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17, Runs: nil,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs at least one run")
	})

	t.Run("refuses a run whose policy version disagrees with the row key (no cross-version merge)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("run_17", 17, nil),
				makeRun("run_18", 18, nil),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ONE repository at ONE policy version")
	})

	t.Run("refuses containment violations (abstains beyond decisions, audited beyond sampled)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("r1", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Abstains = 101
				}),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceed decisions")
		_, err = evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("r2", 17, func(r *evaluation.SteadyStateRunInput) {
					r.SpotCheckChecked = 5
				}),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds spotCheckSampled")
	})

	t.Run("refuses to summarize zero evidence (an empty summary is not a healthy system)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{})
		require.Error(t, err)
		_, err = evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			AbstainHealthBand: &evaluation.AbstainHealthBand{MinAbstainRate: f(0.1)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "needs at least one row")
	})

	t.Run("the dashboard renders an explicit NO-DATA state for zero rows instead of a gate verdict", func(t *testing.T) {
		markdown := evaluation.RenderSteadyStateDashboard(nil, nil)
		assert.Contains(t, markdown, "NO DATA")
		assert.Contains(t, markdown, "ADVISORY")
		// Never a gate verdict over zero evidence — in either direction.
		assert.NotContains(t, markdown, "gate floor met")
		assert.NotContains(t, markdown, "GATE FLOOR BREACHED")
	})

	t.Run("refuses autoApplied beyond decisions (no phantom audit population below the floor)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("r_phantom", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Decisions = 10
					r.Abstains = 5
					r.Overrides = 5
					r.AutoApplied = 1000
				}),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "autoApplied (1000) exceeds decisions (10)")
		// The boundary itself is contained (equal counts are legal evidence).
		boundary, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("r_boundary", 17, func(r *evaluation.SteadyStateRunInput) {
					r.Decisions = 10
					r.Abstains = 5
					r.Overrides = 5
					r.AutoApplied = 10
					r.SpotCheckSampled = 2
					r.SpotCheckChecked = 1
				}),
			},
		})
		require.NoError(t, err)
		assert.Equal(t, 10, boundary.SpotCheck.AutoApplied)
		assert.Equal(t, 1, boundary.RunCount)
	})

	t.Run("refuses a repeated run id inside one row (no double-counted totals)", func(t *testing.T) {
		_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
			Repo: repo, PolicyVersion: 17,
			Runs: []evaluation.SteadyStateRunInput{
				makeRun("run_dupe", 17, nil),
				makeRun("run_dupe", 17, nil),
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "run_dupe")
	})

	t.Run("a row with distinct run ids keeps the totals and the attribution provenance in agreement", func(t *testing.T) {
		row := makeRow(t, repo, 17, []evaluation.SteadyStateRunInput{
			makeRun("run_dupe_a", 17, func(r *evaluation.SteadyStateRunInput) {
				r.Usages = []evaluation.CostAttributionUsage{
					usageRow("writer", "anthropic/claude-sonnet-4.5", 100, 20, f(0.05)),
				}
			}),
			makeRun("run_dupe_b", 17, func(r *evaluation.SteadyStateRunInput) {
				r.Usages = []evaluation.CostAttributionUsage{
					usageRow("writer", "anthropic/claude-sonnet-4.5", 100, 20, f(0.05)),
				}
			}),
		})
		attributedRunIDs := map[string]bool{}
		for _, group := range row.CostAttribution {
			for _, runID := range group.RunIDs {
				attributedRunIDs[runID] = true
			}
		}
		summary, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row},
		})
		require.NoError(t, err)
		// The double-counted-total defect this closes: Totals.Runs must equal
		// the number of DISTINCT attributed run ids, never exceed it.
		assert.Equal(t, len(attributedRunIDs), summary.Totals.Runs)
		assert.Equal(t, row.RunCount, summary.Totals.Runs)
	})

	t.Run("refuses a missing row instead of summarizing over it (healthy is never assumed)", func(t *testing.T) {
		runs := attributionRuns()
		row17 := makeRow(t, repo, 17, runs[:2])
		row18 := makeRow(t, repo, 18, runs[2:])
		_, err := evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{row18, nil},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is missing")
		_, err = evaluation.BuildSteadyStateSummary(evaluation.SteadyStateSummaryInput{
			Rows: []*evaluation.SteadyStateRow{nil, row17},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "index 0 is missing")
	})

	t.Run("every containment is enforced: the full chain sampled ≤ autoApplied ≤ decisions", func(t *testing.T) {
		mutations := []func(*evaluation.SteadyStateRunInput){
			func(r *evaluation.SteadyStateRunInput) { r.Abstains = 101 },                             // abstains beyond decisions
			func(r *evaluation.SteadyStateRunInput) { r.Overrides = 101 },                            // overrides beyond decisions
			func(r *evaluation.SteadyStateRunInput) { r.AutoApplied = 101 },                          // auto-applied beyond decisions (F4-2a)
			func(r *evaluation.SteadyStateRunInput) { r.SpotCheckSampled = 41 },                      // sampled beyond auto-applied (40)
			func(r *evaluation.SteadyStateRunInput) { r.SpotCheckChecked = 5 },                       // audited beyond sampled (4)
			func(r *evaluation.SteadyStateRunInput) { r.AuditMispredictions = 5 },                    // mispredicted beyond audited (4)
			func(r *evaluation.SteadyStateRunInput) { r.FalsePositives = 51; r.FalseNegatives = 50 }, // FP+FN beyond decisions (101)
		}
		for index, mutate := range mutations {
			_, err := evaluation.BuildSteadyStateRow(evaluation.SteadyStateRowInput{
				Repo: repo, PolicyVersion: 17,
				Runs: []evaluation.SteadyStateRunInput{makeRun("c"+pad3(index), 17, mutate)},
			})
			require.Error(t, err, "case %d must be refused", index)
		}
	})
}
