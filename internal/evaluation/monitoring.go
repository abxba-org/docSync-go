// Ported from core/src/evaluation/monitoring.ts (P12-T19; ARCH §5.2, §9,
// §10.6; R11, R13): the steady-state monitoring surface.
//
// Once promotion machinery exists, the interesting question moves from "is the
// candidate better?" to "is the RUNNING system healthy?": this module is the
// ONE deterministic surface that aggregates run history into per-policy-version
// views — the abstain-health monitor (R11), the spot-check audit rate (R13) and
// the per-policy-version cost attribution (ARCH §10.6) — as pure DATA plus an
// advisory markdown renderer. It wires together the Phase-10 primitives
// (EvaluateAbstainHealth, SpotCheckSampleSize) instead of re-deriving their
// math, and it NEVER invents a number: thresholds, bands and ceilings are
// caller-supplied DATA (D11), usage sums over gateway-accounted records (ARCH
// §10.6: the usage numbers are the source, never estimated), and every rate
// ships with the counts it was computed from.
//
// PURE and DETERMINISTIC (P19): inputs in, data out — no IO, no clock, no
// randomness. The dashboard renderer is ADVISORY: banner + INV-11 capped
// tables + provenance footer, and it implies no write.
//
// Keying: one row is ONE repository at ONE policy version
// (`{repo}@v{policyVersion}`) because §8.4 attributes every decision to the
// policy version that produced it, and ARCH §10.6 pins displayed confidence to
// (policy_version, model binding) — so cost attribution groups usage by exactly
// that pair. A row whose runs disagree on the key is REFUSED (fail closed),
// never silently merged across versions.
package evaluation

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// SteadyStateRenderCap is the max rows rendered per dashboard table before the
// overflow line (INV-11).
const SteadyStateRenderCap = 50

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func requireNonEmptyString(name, value string) error {
	if value == "" {
		return fmt.Errorf("steady-state monitoring: %s must be a non-empty string", name)
	}
	return nil
}

func requireCount(name string, value int) error {
	if value < 0 {
		return fmt.Errorf("steady-state monitoring: %s must be a non-negative integer, got %d", name, value)
	}
	return nil
}

func requirePositiveInt(name string, value int) error {
	if value < 1 {
		return fmt.Errorf("steady-state monitoring: %s must be a positive integer, got %d", name, value)
	}
	return nil
}

func requireFiniteNonNegative(name string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return fmt.Errorf("steady-state monitoring: %s must be a finite number >= 0, got %s", name, jsNumber(value))
	}
	return nil
}

// share returns the share of part in whole; zero-division safe (never NaN).
func shareOf(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

/* ------------------------------------------------------------------ */
/* Cost attribution (ARCH §10.6; D3 pinning)                          */
/* ------------------------------------------------------------------ */

// CostAttributionUsage is one usage row keyed (role, policy_version, model
// binding) — the shape the Phase-2 telemetry capture produces (P2-T06, ARCH
// §10.6): per-call prompt/completion tokens, the gateway-accounted cost when
// the gateway reports it, and the measured latency. `Model` is the
// ACTUALLY-ROUTED model id — never the role's configured default (D3
// provenance).
type CostAttributionUsage struct {
	// Role is the logical role that made the call (e.g. `decision-engine`,
	// `writer`).
	Role string
	// Model is the actually-routed model id (D3) — the binding half of the
	// attribution key.
	Model string
	// Provider is the provider when telemetry carries it (optional,
	// degradation-tolerant).
	Provider *string
	// PromptTokens are the prompt tokens billed for this call (gateway usage,
	// never estimated).
	PromptTokens int
	// CompletionTokens are the completion tokens billed for this call
	// (gateway usage, never estimated).
	CompletionTokens int
	// CostUsd is the billed USD when the gateway reports it; nil ⇒ not
	// guessed.
	CostUsd *float64
	// LatencyMs is the measured latency in ms when telemetry carries it.
	LatencyMs *float64
}

// CostAttributionRun is one run's contribution to cost attribution.
type CostAttributionRun struct {
	// RunID is the run id (provenance: the attributed runs are named, never
	// anonymous).
	RunID string
	// PolicyVersion is the policy version that produced this run's traffic.
	PolicyVersion int
	// Usages are the per-call usage rows for this run (any order; output is
	// deterministic).
	Usages []CostAttributionUsage
}

// CostAttributionInput is the input record of CostAttributionByPolicyVersion.
type CostAttributionInput struct {
	// Runs are the run records to attribute (any order; output is
	// deterministic).
	Runs []CostAttributionRun
}

// CostAttributionGroup is the usage attributed to ONE (policy_version, model
// binding) pair. Cost is the sum of KNOWN costs only; rows whose gateway
// omitted cost are counted in UsagesWithoutCost so an unknown cost is surfaced
// as unknown — never laundered into 0 (ARCH §10.6).
type CostAttributionGroup struct {
	PolicyVersion        int
	Role                 string
	Model                string
	Provider             *string
	UsageCount           int
	RunIDs               []string
	PromptTokens         int
	CompletionTokens     int
	TotalTokens          int
	TotalCostUsd         float64
	UsagesWithoutCost    int
	TotalLatencyMs       float64
	UsagesWithoutLatency int
}

type groupAccumulator struct {
	policyVersion        int
	role                 string
	model                string
	provider             *string
	usageCount           int
	runIDs               map[string]bool
	promptTokens         int
	completionTokens     int
	totalCostUsd         float64
	usagesWithoutCost    int
	totalLatencyMs       float64
	usagesWithoutLatency int
}

// groupKey renders the attribution key: (policy_version, role, model,
// provider) (ARCH §10.6).
func groupKey(policyVersion int, role, model string, provider *string) string {
	providerText := ""
	if provider != nil {
		providerText = *provider
	}
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", policyVersion, role, model, providerText)
}

func emptyAccumulator(policyVersion int, role, model string, provider *string) *groupAccumulator {
	return &groupAccumulator{
		policyVersion: policyVersion,
		role:          role,
		model:         model,
		provider:      provider,
		runIDs:        map[string]bool{},
	}
}

// finalizeGroup freezes one accumulator into its output shape (sorted run ids).
func finalizeGroup(group *groupAccumulator) CostAttributionGroup {
	runIDs := make([]string, 0, len(group.runIDs))
	for runID := range group.runIDs {
		runIDs = append(runIDs, runID)
	}
	sort.SliceStable(runIDs, func(i, j int) bool { return compareStrings(runIDs[i], runIDs[j]) < 0 })
	return CostAttributionGroup{
		PolicyVersion:        group.policyVersion,
		Role:                 group.role,
		Model:                group.model,
		Provider:             group.provider,
		UsageCount:           group.usageCount,
		RunIDs:               runIDs,
		PromptTokens:         group.promptTokens,
		CompletionTokens:     group.completionTokens,
		TotalTokens:          group.promptTokens + group.completionTokens,
		TotalCostUsd:         group.totalCostUsd,
		UsagesWithoutCost:    group.usagesWithoutCost,
		TotalLatencyMs:       group.totalLatencyMs,
		UsagesWithoutLatency: group.usagesWithoutLatency,
	}
}

// compareGroups is the canonical group ordering: (policy_version, role, model,
// provider).
func compareGroups(a, b CostAttributionGroup) int {
	if a.PolicyVersion != b.PolicyVersion {
		if a.PolicyVersion < b.PolicyVersion {
			return -1
		}
		return 1
	}
	if c := compareStrings(a.Role, b.Role); c != 0 {
		return c
	}
	if c := compareStrings(a.Model, b.Model); c != 0 {
		return c
	}
	return compareStrings(providerOrEmpty(a.Provider), providerOrEmpty(b.Provider))
}

func providerOrEmpty(provider *string) string {
	if provider == nil {
		return ""
	}
	return *provider
}

func validateUsage(runID string, index int, usage CostAttributionUsage) error {
	where := fmt.Sprintf("cost attribution: run %s usage %d", runID, index)
	if err := requireNonEmptyString(where+" role", usage.Role); err != nil {
		return err
	}
	if err := requireNonEmptyString(where+" model", usage.Model); err != nil {
		return err
	}
	if usage.Provider != nil {
		if err := requireNonEmptyString(where+" provider", *usage.Provider); err != nil {
			return err
		}
	}
	if err := requireCount(where+" promptTokens", usage.PromptTokens); err != nil {
		return err
	}
	if err := requireCount(where+" completionTokens", usage.CompletionTokens); err != nil {
		return err
	}
	if usage.CostUsd != nil {
		if err := requireFiniteNonNegative(where+" costUsd", *usage.CostUsd); err != nil {
			return err
		}
	}
	if usage.LatencyMs != nil {
		if err := requireFiniteNonNegative(where+" latencyMs", *usage.LatencyMs); err != nil {
			return err
		}
	}
	return nil
}

// CostAttributionByPolicyVersion groups run records by (policy_version, model
// binding) and sums usage (P12-T19; ARCH §10.6): the telemetry keying P2-T06
// captures (role, policy_version, model binding) IS the attribution key, so a
// cost is always attributable to the exact policy version and binding that
// spent it — and the confidence a dashboard displays stays pinned to that same
// pair (D3). Pure and deterministic: groups sorted by (policy_version, role,
// model, provider), run ids sorted; same input ⇒ deep-equal output.
func CostAttributionByPolicyVersion(runs []CostAttributionRun) ([]CostAttributionGroup, error) {
	groups := map[string]*groupAccumulator{}
	for _, run := range runs {
		if err := requireNonEmptyString("cost attribution: runId", run.RunID); err != nil {
			return nil, err
		}
		if err := requirePositiveInt(fmt.Sprintf("cost attribution: run %s policyVersion", run.RunID), run.PolicyVersion); err != nil {
			return nil, err
		}
		for index, usage := range run.Usages {
			if err := validateUsage(run.RunID, index, usage); err != nil {
				return nil, err
			}
			provider := usage.Provider
			key := groupKey(run.PolicyVersion, usage.Role, usage.Model, provider)
			existing, ok := groups[key]
			if !ok {
				existing = emptyAccumulator(run.PolicyVersion, usage.Role, usage.Model, provider)
			}
			existing.usageCount++
			existing.runIDs[run.RunID] = true
			existing.promptTokens += usage.PromptTokens
			existing.completionTokens += usage.CompletionTokens
			if usage.CostUsd == nil {
				existing.usagesWithoutCost++
			} else {
				existing.totalCostUsd += *usage.CostUsd
			}
			if usage.LatencyMs == nil {
				existing.usagesWithoutLatency++
			} else {
				existing.totalLatencyMs += *usage.LatencyMs
			}
			groups[key] = existing
		}
	}
	out := make([]CostAttributionGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, finalizeGroup(group))
	}
	sort.SliceStable(out, func(i, j int) bool { return compareGroups(out[i], out[j]) < 0 })
	return out, nil
}

// MergeCostAttribution merges already-attributed groups (e.g. per-row
// attributions) back onto the (policy_version, model binding) key, preserving
// every count — usage rows, known cost, unknown-cost rows, latencies and the
// contributing run ids — so a merged total is never more optimistic than its
// parts.
func MergeCostAttribution(groups []CostAttributionGroup) ([]CostAttributionGroup, error) {
	merged := map[string]*groupAccumulator{}
	for _, group := range groups {
		key := groupKey(group.PolicyVersion, group.Role, group.Model, group.Provider)
		existing, ok := merged[key]
		if !ok {
			existing = emptyAccumulator(group.PolicyVersion, group.Role, group.Model, group.Provider)
		}
		existing.usageCount += group.UsageCount
		for _, runID := range group.RunIDs {
			existing.runIDs[runID] = true
		}
		existing.promptTokens += group.PromptTokens
		existing.completionTokens += group.CompletionTokens
		existing.totalCostUsd += group.TotalCostUsd
		existing.usagesWithoutCost += group.UsagesWithoutCost
		existing.totalLatencyMs += group.TotalLatencyMs
		existing.usagesWithoutLatency += group.UsagesWithoutLatency
		merged[key] = existing
	}
	out := make([]CostAttributionGroup, 0, len(merged))
	for _, group := range merged {
		out = append(out, finalizeGroup(group))
	}
	sort.SliceStable(out, func(i, j int) bool { return compareGroups(out[i], out[j]) < 0 })
	return out, nil
}

/* ------------------------------------------------------------------ */
/* Spot-check + audit ingestion helpers (R13; ARCH §9; OD-R25)         */
/* ------------------------------------------------------------------ */

// auditMispredictionLabels are the misprediction-bearing audit labels
// (P10-T11 verdict mapping, OD-R25).
var auditMispredictionLabels = map[string]bool{
	string(schemas.HygieneLabelCorrected):     true,
	string(schemas.HygieneLabelFalsePositive): true,
}

// CountAuditMispredictions counts the mispredictions inside an ingested
// audit-finding label stream: the `audit_finding`-sourced hygiene labels whose
// verdict found the patch wrong (`patch_incorrect` ⇒ `corrected`) or the update
// unwarranted (`update_unwarranted` ⇒ `false_positive`). Reuses the Phase-10
// ingestion vocabulary (AuditFindingSourceSignal) so the monitoring surface
// reads the SAME ground-truth stream the evaluation loop does — audit evidence
// never enters as a side channel.
func CountAuditMispredictions(labels []schemas.HygieneLabel) int {
	count := 0
	for _, label := range labels {
		if label.SourceSignal == schemas.HygieneSourceAuditFinding &&
			auditMispredictionLabels[string(label.Label)] {
			count++
		}
	}
	return count
}

// SpotCheckSummary is the spot-check audit state of one row (counts + the
// rates derived from them).
type SpotCheckSummary struct {
	// AutoApplied counts the patches this row's runs applied automatically
	// (the audit population).
	AutoApplied int
	// Sampled counts the patches the seeded sampler selected for audit
	// (OD-R25).
	Sampled int
	// Checked counts the audits that actually completed with a verdict.
	Checked int
	// Mispredictions counts the completed audits that found a
	// misprediction.
	Mispredictions int
	// ExpectedSampleSize is the Phase-10 sampler expectation for this batch
	// (SpotCheckSampleSize).
	ExpectedSampleSize int
	// SampledRate is sampled ÷ autoApplied (0 when nothing was
	// auto-applied).
	SampledRate float64
	// CheckedRate is checked ÷ sampled (0 when nothing was sampled).
	CheckedRate float64
	// MispredictionRate is mispredictions ÷ checked (0 when nothing was
	// audited).
	MispredictionRate float64
	// SampleFloorMet is true when the sample meets the Phase-10 floor for the
	// batch (R13).
	SampleFloorMet bool
}

// AbstainHealthSummary is the 2-D abstain-health read of one row (Phase-10
// monitor, R11).
type AbstainHealthSummary struct {
	// AbstainRate is abstains ÷ decisions (0 when the row decided nothing).
	AbstainRate float64
	// OverrideRate is overrides ÷ decisions (0 when the row decided nothing).
	OverrideRate float64
	// Decisions is the denominator both rates were computed over.
	Decisions int
	// Alert is the Phase-10 2-D monitor verdict (policy_gap /
	// overconfidence / none — nil when no alert fired).
	Alert *AbstainHealthAlert
}

// SteadyStateRowFlag values carried by a SteadyStateRow.
const SteadyStateRowFlagUnsafeAutoUpdates = "unsafe_auto_updates"

// SteadyStateRow is one repository (or run key) at ONE policy version — the
// steady-state monitoring unit (P12-T19; ARCH §5.2, §9, §10.6; R11, R13). It
// carries the raw counts next to every derived rate (a rate without its
// denominator is not evidence), the Phase-10 abstain-health alert, the
// spot-check audit state, the FP/FN rates, the unsafe-auto-update count (the
// ARCH §8.6 gate floor — any nonzero value flags the row) and the per-binding
// cost attribution.
type SteadyStateRow struct {
	Key                string
	Repo               string
	PolicyVersion      int
	RunCount           int
	DecisionCount      int
	AbstainCount       int
	OverrideCount      int
	AbstainHealth      AbstainHealthSummary
	SpotCheck          SpotCheckSummary
	FalsePositiveCount int
	FalseNegativeCount int
	FalsePositiveRate  float64
	FalseNegativeRate  float64
	UnsafeAutoUpdates  int
	Flags              []string
	CostAttribution    []CostAttributionGroup
}

// SteadyStateRunInput is one run's contribution to a steady-state row.
type SteadyStateRunInput struct {
	RunID               string
	Repo                string
	PolicyVersion       int
	Decisions           int
	Abstains            int
	Overrides           int
	AutoApplied         int
	SpotCheckSampled    int
	SpotCheckChecked    int
	AuditMispredictions int
	FalsePositives      int
	FalseNegatives      int
	UnsafeAutoUpdates   int
	Usages              []CostAttributionUsage
}

// SteadyStateRowInput is the input of SteadyStateRow.
type SteadyStateRowInput struct {
	Repo          string
	PolicyVersion int
	// Runs are the runs in this row's window (must all share the row's repo +
	// version).
	Runs []SteadyStateRunInput
}

// toAttributionRuns converts steady-state run inputs onto the cost-attribution
// run shape (the TS structural typing made this implicit).
func toAttributionRuns(runs []SteadyStateRunInput) []CostAttributionRun {
	out := make([]CostAttributionRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, CostAttributionRun{
			RunID:         run.RunID,
			PolicyVersion: run.PolicyVersion,
			Usages:        run.Usages,
		})
	}
	return out
}

// CostAttributionRunsFrom exposes the steady-state run → cost-attribution run
// conversion (the TS caller passed structurally-compatible run objects
// directly; Go needs the explicit shape).
func CostAttributionRunsFrom(runs []SteadyStateRunInput) []CostAttributionRun {
	return toAttributionRuns(runs)
}

// SteadyStateRowKey renders the deterministic row key: `{repo}@v{policyVersion}`.
func SteadyStateRowKey(repo string, policyVersion int) string {
	return fmt.Sprintf("%s@v%d", repo, policyVersion)
}

// BuildSteadyStateRow aggregates one (repo, policy version) window into a
// SteadyStateRow (the TS steadyStateRow function; Go cannot share the type
// name, so the constructor carries the Build prefix).
//
// Fail closed: refuses an empty window and any run whose repo/policy version
// disagrees with the row key (a row is ONE repository at ONE policy version —
// merging across versions would fabricate attribution), refuses a repeated
// run id inside the window (it would double-count every total while the cost
// attribution dedupes run ids, so the totals would disagree with their own
// provenance), refuses containment violations (abstains/overrides beyond
// decisions, auto-applied beyond decisions, sampled beyond auto-applied,
// audited beyond sampled, mispredicted beyond audited, FP+FN beyond
// decisions). The abstain-health alert is the Phase-10 2-D monitor
// (EvaluateAbstainHealth) reused verbatim and the expected sample size is the
// Phase-10 sampler (SpotCheckSampleSize) reused verbatim — no metric math is
// re-derived.
func BuildSteadyStateRow(input SteadyStateRowInput) (*SteadyStateRow, error) {
	repo := input.Repo
	if err := requireNonEmptyString("steadyStateRow: repo", repo); err != nil {
		return nil, err
	}
	if err := requirePositiveInt("steadyStateRow: policyVersion", input.PolicyVersion); err != nil {
		return nil, err
	}
	where := fmt.Sprintf("steadyStateRow: %s", SteadyStateRowKey(repo, input.PolicyVersion))
	if len(input.Runs) == 0 {
		return nil, fmt.Errorf("%s needs at least one run — an empty row has no evidence", where)
	}

	runCount := 0
	decisions := 0
	abstains := 0
	overrides := 0
	autoApplied := 0
	sampled := 0
	checked := 0
	mispredictions := 0
	falsePositives := 0
	falseNegatives := 0
	unsafeAutoUpdates := 0
	seenRunIDs := map[string]bool{}

	for _, run := range input.Runs {
		if err := requireNonEmptyString(where+" runId", run.RunID); err != nil {
			return nil, err
		}
		if seenRunIDs[run.RunID] {
			return nil, fmt.Errorf("%s: run id %s appears more than once — a repeated run double-counts every total while the cost attribution dedupes run ids (fail closed)",
				where, run.RunID)
		}
		seenRunIDs[run.RunID] = true
		if run.Repo != repo || run.PolicyVersion != input.PolicyVersion {
			return nil, fmt.Errorf("%s: run %s is keyed %s — a steady-state row is ONE repository at ONE policy version (fail closed)",
				where, run.RunID, SteadyStateRowKey(run.Repo, run.PolicyVersion))
		}
		runDecisions := run.Decisions
		runAbstains := run.Abstains
		runOverrides := run.Overrides
		runAutoApplied := run.AutoApplied
		runSampled := run.SpotCheckSampled
		runChecked := run.SpotCheckChecked
		runMispredictions := run.AuditMispredictions
		runFalsePositives := run.FalsePositives
		runFalseNegatives := run.FalseNegatives
		runUnsafe := run.UnsafeAutoUpdates
		if err := requireCount(where+" decisions", runDecisions); err != nil {
			return nil, err
		}
		if err := requireCount(where+" abstains", runAbstains); err != nil {
			return nil, err
		}
		if err := requireCount(where+" overrides", runOverrides); err != nil {
			return nil, err
		}
		if err := requireCount(where+" autoApplied", runAutoApplied); err != nil {
			return nil, err
		}
		if err := requireCount(where+" spotCheckSampled", runSampled); err != nil {
			return nil, err
		}
		if err := requireCount(where+" spotCheckChecked", runChecked); err != nil {
			return nil, err
		}
		if err := requireCount(where+" auditMispredictions", runMispredictions); err != nil {
			return nil, err
		}
		if err := requireCount(where+" falsePositives", runFalsePositives); err != nil {
			return nil, err
		}
		if err := requireCount(where+" falseNegatives", runFalseNegatives); err != nil {
			return nil, err
		}
		if err := requireCount(where+" unsafeAutoUpdates", runUnsafe); err != nil {
			return nil, err
		}
		if runAbstains > runDecisions {
			return nil, fmt.Errorf("%s: abstains (%d) exceed decisions (%d)", where, runAbstains, runDecisions)
		}
		if runOverrides > runDecisions {
			return nil, fmt.Errorf("%s: overrides (%d) exceed decisions (%d)", where, runOverrides, runDecisions)
		}
		if runAutoApplied > runDecisions {
			return nil, fmt.Errorf("%s: autoApplied (%d) exceeds decisions (%d)", where, runAutoApplied, runDecisions)
		}
		if runSampled > runAutoApplied {
			return nil, fmt.Errorf("%s: spotCheckSampled (%d) exceeds autoApplied (%d)", where, runSampled, runAutoApplied)
		}
		if runChecked > runSampled {
			return nil, fmt.Errorf("%s: spotCheckChecked (%d) exceeds spotCheckSampled (%d)", where, runChecked, runSampled)
		}
		if runMispredictions > runChecked {
			return nil, fmt.Errorf("%s: auditMispredictions (%d) exceed spotCheckChecked (%d)", where, runMispredictions, runChecked)
		}
		if runFalsePositives+runFalseNegatives > runDecisions {
			return nil, fmt.Errorf("%s: falsePositives + falseNegatives (%d) exceed decisions (%d)",
				where, runFalsePositives+runFalseNegatives, runDecisions)
		}
		runCount++
		decisions += runDecisions
		abstains += runAbstains
		overrides += runOverrides
		autoApplied += runAutoApplied
		sampled += runSampled
		checked += runChecked
		mispredictions += runMispredictions
		falsePositives += runFalsePositives
		falseNegatives += runFalseNegatives
		unsafeAutoUpdates += runUnsafe
	}

	abstainRate := shareOf(abstains, decisions)
	overrideRate := shareOf(overrides, decisions)
	expectedSampleSize, err := SpotCheckSampleSize(autoApplied)
	if err != nil {
		return nil, err
	}

	var alert *AbstainHealthAlert
	if value, fired := EvaluateAbstainHealth(abstainRate, overrideRate); fired {
		alertCopy := value
		alert = &alertCopy
	}

	costAttribution, err := CostAttributionByPolicyVersion(toAttributionRuns(input.Runs))
	if err != nil {
		return nil, err
	}

	flags := []string{}
	if unsafeAutoUpdates > 0 {
		flags = append(flags, SteadyStateRowFlagUnsafeAutoUpdates)
	}

	return &SteadyStateRow{
		Key:           SteadyStateRowKey(repo, input.PolicyVersion),
		Repo:          repo,
		PolicyVersion: input.PolicyVersion,
		RunCount:      runCount,
		DecisionCount: decisions,
		AbstainCount:  abstains,
		OverrideCount: overrides,
		AbstainHealth: AbstainHealthSummary{
			AbstainRate:  abstainRate,
			OverrideRate: overrideRate,
			Decisions:    decisions,
			Alert:        alert,
		},
		SpotCheck: SpotCheckSummary{
			AutoApplied:        autoApplied,
			Sampled:            sampled,
			Checked:            checked,
			Mispredictions:     mispredictions,
			ExpectedSampleSize: expectedSampleSize,
			SampledRate:        shareOf(sampled, autoApplied),
			CheckedRate:        shareOf(checked, sampled),
			MispredictionRate:  shareOf(mispredictions, checked),
			SampleFloorMet:     sampled >= expectedSampleSize,
		},
		FalsePositiveCount: falsePositives,
		FalseNegativeCount: falseNegatives,
		FalsePositiveRate:  shareOf(falsePositives, decisions),
		FalseNegativeRate:  shareOf(falseNegatives, decisions),
		UnsafeAutoUpdates:  unsafeAutoUpdates,
		Flags:              flags,
		CostAttribution:    costAttribution,
	}, nil
}

/* ------------------------------------------------------------------ */
/* Steady-state summary (the aggregate monitor view)                   */
/* ------------------------------------------------------------------ */

// AbstainHealthBand is the caller-supplied abstain-health band (DATA — D11: a
// repository calibrates its own band on its evaluation split; this module
// never hard-codes one, the way the 0.94/0.72 routing thresholds are never
// hard-coded). Bounds are inclusive.
type AbstainHealthBand struct {
	// MinAbstainRate is the abstain rate below which the row is flagged
	// `below_min` (overconfidence).
	MinAbstainRate *float64
	// MaxAbstainRate is the abstain rate above which the row is flagged
	// `above_max` (policy gap).
	MaxAbstainRate *float64
}

// SteadyStateTotals carries the aggregate totals across the summary's rows.
type SteadyStateTotals struct {
	Runs                int
	Decisions           int
	Abstains            int
	Overrides           int
	AutoApplied         int
	SpotCheckSampled    int
	SpotCheckChecked    int
	AuditMispredictions int
	FalsePositives      int
	FalseNegatives      int
	UnsafeAutoUpdates   int
	UsageRecords        int
	TotalTokens         int
	TotalCostUsd        float64
	UsagesWithoutCost   int
}

// AbstainHealthDriftFlag is one row whose abstain rate drifted outside the
// caller-supplied band.
type AbstainHealthDriftFlag struct {
	Key           string
	Repo          string
	PolicyVersion int
	AbstainRate   float64
	Direction     string // 'below_min' | 'above_max'
}

// Abstain-health drift directions.
const (
	DriftDirectionBelowMin = "below_min"
	DriftDirectionAboveMax = "above_max"
)

// AbstainHealthAlertEntry is one Phase-10 2-D abstain-health alert observed
// across the rows (R11).
type AbstainHealthAlertEntry struct {
	Key           string
	Repo          string
	PolicyVersion int
	Alert         AbstainHealthAlert
}

// Spot-check coverage gap reasons (R13's selection-bias guard).
const (
	SpotCheckGapNoSample         = "no_sample"
	SpotCheckGapNoAudit          = "no_audit"
	SpotCheckGapNoAutoApplyBatch = "no_auto_apply_batch"
)

// SpotCheckCoverageGap is one row missing spot-check evidence (R13's
// selection-bias guard).
type SpotCheckCoverageGap struct {
	Key           string
	Repo          string
	PolicyVersion int
	Reason        string
}

// SteadyStateSummaryInput is the input of SteadyStateSummary.
type SteadyStateSummaryInput struct {
	// Rows are the steady-state rows to summarize.
	Rows []*SteadyStateRow
	// AbstainHealthBand is the caller-supplied abstain-health band (DATA,
	// D11); nil ⇒ no band flags.
	AbstainHealthBand *AbstainHealthBand
}

// SteadyStateSummary is the aggregate steady-state view across rows (P12-T19).
type SteadyStateSummary struct {
	RowCount      int
	Totals        SteadyStateTotals
	AbstainHealth struct {
		// Band is the band the monitor ran against (echoed; nil when not
		// configured).
		Band *AbstainHealthBand
		// DriftFlags lists the rows whose abstain rate drifted outside the
		// band (caller-supplied data).
		DriftFlags []AbstainHealthDriftFlag
		// Alerts surfaces the Phase-10 2-D alerts from the rows (R11).
		Alerts []AbstainHealthAlertEntry
		// InBand is true when no row drifted outside the band.
		InBand bool
	}
	SpotCheckCoverage struct {
		// Gaps lists the rows with zero spot-checks — flagged, never silently
		// dropped (R13).
		Gaps        []SpotCheckCoverageGap
		AutoApplied int
		Sampled     int
		Checked     int
		// CoverageOfSample is checked ÷ sampled (audit completion over the
		// sample).
		CoverageOfSample float64
		// SampledShareOfAutoApplied is sampled ÷ autoApplied (the OD-R25
		// selection share).
		SampledShareOfAutoApplied float64
		// Complete is true when every row has spot-check evidence.
		Complete bool
	}
	// CostAttribution is the merged per-(policy_version, model binding)
	// attribution across all rows.
	CostAttribution []CostAttributionGroup
	// UnsafeAutoUpdatesTotal is the total unsafe auto-updates; ANY nonzero
	// value makes the summary unhealthy.
	UnsafeAutoUpdatesTotal int
	// UnsafeRows lists the row keys carrying the `unsafe_auto_updates` flag
	// (gate provenance).
	UnsafeRows []string
	// Healthy is the ARCH §8.6 gate floor: true iff UnsafeAutoUpdatesTotal
	// === 0. The abstain band and spot-check monitors are ADVISORY sections
	// with their own booleans (InBand, Complete) — repo-calibrated data never
	// silently rewrites the safety floor, and the floor never launders a
	// monitor gap.
	Healthy bool
}

func validateBand(band *AbstainHealthBand) (*AbstainHealthBand, error) {
	if band == nil {
		return nil, nil
	}
	if band.MinAbstainRate == nil && band.MaxAbstainRate == nil {
		return nil, fmt.Errorf("steadyStateSummary: abstainHealthBand must supply at least one of minAbstainRate / maxAbstainRate (an empty band is a silent no-op — refused)")
	}
	if band.MinAbstainRate != nil && (math.IsNaN(*band.MinAbstainRate) || math.IsInf(*band.MinAbstainRate, 0) || *band.MinAbstainRate < 0 || *band.MinAbstainRate > 1) {
		return nil, fmt.Errorf("steadyStateSummary: abstainHealthBand.minAbstainRate must be in [0, 1], got %s", jsNumber(*band.MinAbstainRate))
	}
	if band.MaxAbstainRate != nil && (math.IsNaN(*band.MaxAbstainRate) || math.IsInf(*band.MaxAbstainRate, 0) || *band.MaxAbstainRate < 0 || *band.MaxAbstainRate > 1) {
		return nil, fmt.Errorf("steadyStateSummary: abstainHealthBand.maxAbstainRate must be in [0, 1], got %s", jsNumber(*band.MaxAbstainRate))
	}
	if band.MinAbstainRate != nil && band.MaxAbstainRate != nil && *band.MinAbstainRate > *band.MaxAbstainRate {
		return nil, fmt.Errorf("steadyStateSummary: abstainHealthBand min (%s) exceeds max (%s) — refused",
			jsNumber(*band.MinAbstainRate), jsNumber(*band.MaxAbstainRate))
	}
	return band, nil
}

// BuildSteadyStateSummary computes the aggregate monitor view across
// SteadyStateRows (the TS steadyStateSummary function; Go cannot share the
// type name, so the constructor carries the Build prefix): totals, the
// abstain-health monitor (rows outside the CALLER-SUPPLIED band are flagged —
// the band is data, D11), the spot-check coverage guard (rows with zero
// spot-checks are flagged — R13's selection-bias guard), the merged
// per-policy-version cost attribution (ARCH §10.6), and the unsafe-auto-updates
// gate floor (UnsafeAutoUpdatesTotal; any nonzero ⇒ Healthy false, ARCH §8.6 /
// V-P12-4). Pure and deterministic: every list is sorted, same input ⇒
// deep-equal output.
//
// FAIL CLOSED AT CONSTRUCTION: an EMPTY summary is refused (`rows: []` has no
// evidence, so an all-green summary over zero rows would launder "no data"
// into "gate floor met" — the same reason SteadyStateRow refuses an empty
// window), and a missing row (nil entry) is refused rather than skipped.
// `Healthy` can therefore never be true when the evidence is absent: the
// function returns an error instead of an optimistic verdict.
func BuildSteadyStateSummary(input SteadyStateSummaryInput) (*SteadyStateSummary, error) {
	if len(input.Rows) == 0 {
		return nil, fmt.Errorf("steadyStateSummary: needs at least one row — an empty summary has no evidence, and zero evidence is never a healthy system (fail closed)")
	}
	band, err := validateBand(input.AbstainHealthBand)
	if err != nil {
		return nil, err
	}

	driftFlags := []AbstainHealthDriftFlag{}
	alerts := []AbstainHealthAlertEntry{}
	gaps := []SpotCheckCoverageGap{}
	unsafeRows := []string{}

	runs := 0
	decisions := 0
	abstains := 0
	overrides := 0
	autoApplied := 0
	sampled := 0
	checked := 0
	mispredictions := 0
	falsePositives := 0
	falseNegatives := 0
	unsafeAutoUpdates := 0

	for index, row := range input.Rows {
		if row == nil {
			return nil, fmt.Errorf("steadyStateSummary: row at index %d is missing — a summary over a missing row has no evidence (fail closed)", index)
		}
		runs += row.RunCount
		decisions += row.DecisionCount
		abstains += row.AbstainCount
		overrides += row.OverrideCount
		autoApplied += row.SpotCheck.AutoApplied
		sampled += row.SpotCheck.Sampled
		checked += row.SpotCheck.Checked
		mispredictions += row.SpotCheck.Mispredictions
		falsePositives += row.FalsePositiveCount
		falseNegatives += row.FalseNegativeCount
		unsafeAutoUpdates += row.UnsafeAutoUpdates

		if band != nil && band.MinAbstainRate != nil && row.AbstainHealth.AbstainRate < *band.MinAbstainRate {
			driftFlags = append(driftFlags, AbstainHealthDriftFlag{
				Key:           row.Key,
				Repo:          row.Repo,
				PolicyVersion: row.PolicyVersion,
				AbstainRate:   row.AbstainHealth.AbstainRate,
				Direction:     DriftDirectionBelowMin,
			})
		}
		if band != nil && band.MaxAbstainRate != nil && row.AbstainHealth.AbstainRate > *band.MaxAbstainRate {
			driftFlags = append(driftFlags, AbstainHealthDriftFlag{
				Key:           row.Key,
				Repo:          row.Repo,
				PolicyVersion: row.PolicyVersion,
				AbstainRate:   row.AbstainHealth.AbstainRate,
				Direction:     DriftDirectionAboveMax,
			})
		}
		if row.AbstainHealth.Alert != nil {
			alerts = append(alerts, AbstainHealthAlertEntry{
				Key:           row.Key,
				Repo:          row.Repo,
				PolicyVersion: row.PolicyVersion,
				Alert:         *row.AbstainHealth.Alert,
			})
		}
		if row.SpotCheck.Checked == 0 {
			reason := SpotCheckGapNoAudit
			switch {
			case row.SpotCheck.AutoApplied == 0:
				reason = SpotCheckGapNoAutoApplyBatch
			case row.SpotCheck.Sampled == 0:
				reason = SpotCheckGapNoSample
			}
			gaps = append(gaps, SpotCheckCoverageGap{
				Key:           row.Key,
				Repo:          row.Repo,
				PolicyVersion: row.PolicyVersion,
				Reason:        reason,
			})
		}
		if row.UnsafeAutoUpdates > 0 {
			unsafeRows = append(unsafeRows, row.Key)
		}
	}

	sort.SliceStable(driftFlags, func(i, j int) bool {
		if c := compareStrings(driftFlags[i].Key, driftFlags[j].Key); c != 0 {
			return c < 0
		}
		return compareStrings(driftFlags[i].Direction, driftFlags[j].Direction) < 0
	})
	sort.SliceStable(alerts, func(i, j int) bool {
		if c := compareStrings(alerts[i].Key, alerts[j].Key); c != 0 {
			return c < 0
		}
		return compareStrings(string(alerts[i].Alert), string(alerts[j].Alert)) < 0
	})
	sort.SliceStable(gaps, func(i, j int) bool {
		if c := compareStrings(gaps[i].Key, gaps[j].Key); c != 0 {
			return c < 0
		}
		return compareStrings(gaps[i].Reason, gaps[j].Reason) < 0
	})
	sort.SliceStable(unsafeRows, func(i, j int) bool { return compareStrings(unsafeRows[i], unsafeRows[j]) < 0 })

	// Merged per-(policy_version, model binding) attribution across every row.
	allGroups := []CostAttributionGroup{}
	for _, row := range input.Rows {
		if row == nil {
			continue
		}
		allGroups = append(allGroups, row.CostAttribution...)
	}
	costAttribution, err := MergeCostAttribution(allGroups)
	if err != nil {
		return nil, err
	}

	usageRecords := 0
	totalTokens := 0
	totalCostUsd := 0.0
	usagesWithoutCost := 0
	for _, group := range costAttribution {
		usageRecords += group.UsageCount
		totalTokens += group.TotalTokens
		totalCostUsd += group.TotalCostUsd
		usagesWithoutCost += group.UsagesWithoutCost
	}

	summary := &SteadyStateSummary{
		RowCount: len(input.Rows),
		Totals: SteadyStateTotals{
			Runs:                runs,
			Decisions:           decisions,
			Abstains:            abstains,
			Overrides:           overrides,
			AutoApplied:         autoApplied,
			SpotCheckSampled:    sampled,
			SpotCheckChecked:    checked,
			AuditMispredictions: mispredictions,
			FalsePositives:      falsePositives,
			FalseNegatives:      falseNegatives,
			UnsafeAutoUpdates:   unsafeAutoUpdates,
			UsageRecords:        usageRecords,
			TotalTokens:         totalTokens,
			TotalCostUsd:        totalCostUsd,
			UsagesWithoutCost:   usagesWithoutCost,
		},
		AbstainHealth: struct {
			Band       *AbstainHealthBand
			DriftFlags []AbstainHealthDriftFlag
			Alerts     []AbstainHealthAlertEntry
			InBand     bool
		}{
			Band:       band,
			DriftFlags: driftFlags,
			Alerts:     alerts,
			InBand:     len(driftFlags) == 0,
		},
		SpotCheckCoverage: struct {
			Gaps                      []SpotCheckCoverageGap
			AutoApplied               int
			Sampled                   int
			Checked                   int
			CoverageOfSample          float64
			SampledShareOfAutoApplied float64
			Complete                  bool
		}{
			Gaps:                      gaps,
			AutoApplied:               autoApplied,
			Sampled:                   sampled,
			Checked:                   checked,
			CoverageOfSample:          shareOf(checked, sampled),
			SampledShareOfAutoApplied: shareOf(sampled, autoApplied),
			Complete:                  len(gaps) == 0,
		},
		CostAttribution:        costAttribution,
		UnsafeAutoUpdatesTotal: unsafeAutoUpdates,
		UnsafeRows:             unsafeRows,
		Healthy:                unsafeAutoUpdates == 0,
	}
	return summary, nil
}

/* ------------------------------------------------------------------ */
/* Advisory dashboard renderer (INV-11; no writes)                     */
/* ------------------------------------------------------------------ */

const advisoryBanner = "> **ADVISORY — steady-state monitoring dashboard (P12-T19; ARCH §5.2, §9, §10.6).** " +
	"No document was modified by this view and it writes nothing: rows are pure " +
	"aggregates over committed run history. Findings route through the normal " +
	"policy evaluation + promotion pipeline (human-approved), never from here."

// cell escapes markdown table cells (pipes/newlines would break the table).
func cell(value string) string {
	replaced := strings.ReplaceAll(value, "|", `\|`)
	replaced = strings.ReplaceAll(replaced, "\r\n", " ")
	replaced = strings.ReplaceAll(replaced, "\n", " ")
	replaced = strings.ReplaceAll(replaced, "\r", " ")
	return replaced
}

// formatRate renders toFixed(3).
func formatRate(rate float64) string {
	return strconv.FormatFloat(rate, 'f', 3, 64)
}

// formatCost renders `$` + toFixed(4).
func formatCost(costUsd float64) string {
	return fmt.Sprintf("$%s", strconv.FormatFloat(costUsd, 'f', 4, 64))
}

// sortedRows orders rows by (repo, policy version) for deterministic output.
func sortedRows(rows []*SteadyStateRow) []*SteadyStateRow {
	out := make([]*SteadyStateRow, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		if c := compareStrings(out[i].Repo, out[j].Repo); c != 0 {
			return c < 0
		}
		return out[i].PolicyVersion < out[j].PolicyVersion
	})
	return out
}

// RenderSteadyStateDashboard renders the steady-state monitoring dashboard as
// markdown (P12-T19). Advisory, deterministic, READ-ONLY: the banner declares
// that nothing was modified and no write is implied; both tables are capped at
// SteadyStateRenderCap with an explicit overflow line naming the archival
// store (INV-11 — never a silent truncation); the footer carries provenance
// with NO generated clock (same rows ⇒ byte-identical output, P19).
//
// `abstainHealthBand` is optional caller-supplied DATA (D11): when supplied,
// the abstain-health monitor line reports drift against that band; when omitted
// the renderer states the band is not configured instead of inventing one.
//
// Zero rows render an explicit NO DATA state (never a gate verdict): the
// underlying summary refuses an empty row set (fail closed — zero evidence is
// not a healthy system), so the renderer states that directly instead of
// implying a met ARCH §8.6 floor.
func RenderSteadyStateDashboard(rows []*SteadyStateRow, abstainHealthBand *AbstainHealthBand) string {
	view := sortedRows(rows)
	if len(view) == 0 {
		return strings.Join([]string{
			"## Steady-state monitoring dashboard (advisory)",
			"",
			advisoryBanner,
			"",
			"**No steady-state rows — NO DATA.** The monitor refuses to summarize an empty " +
				"window (zero evidence is never a healthy system, fail closed), so no gate verdict " +
				"is rendered here: treat this as UNKNOWN, never as a met ARCH §8.6 floor.",
			"",
			fmt.Sprintf("_Provenance: deterministic render of 0 steady-state row(s) · render cap "+
				"%d (INV-11) · P12-T19 (ARCH §5.2, §9, §10.6; R11, R13). No clock is "+
				"consulted — the same rows render byte-identical output._", SteadyStateRenderCap),
		}, "\n")
	}

	summary, err := BuildSteadyStateSummary(SteadyStateSummaryInput{
		Rows:              view,
		AbstainHealthBand: abstainHealthBand,
	})
	if err != nil {
		// Unreachable for rows produced by SteadyStateRow (fail-closed
		// construction guarantees a valid, non-empty row set).
		return "## Steady-state monitoring dashboard (advisory)\n\n" + advisoryBanner +
			"\n\n**The monitor refused to summarize these rows (fail closed): " + err.Error() + "**\n"
	}

	versionSet := map[int]bool{}
	for _, row := range view {
		versionSet[row.PolicyVersion] = true
	}
	versions := make([]int, 0, len(versionSet))
	for version := range versionSet {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	versionTexts := make([]string, 0, len(versions))
	for _, version := range versions {
		versionTexts = append(versionTexts, fmt.Sprintf("v%d", version))
	}
	versionsText := strings.Join(versionTexts, ", ")
	if versionsText == "" {
		versionsText = "—"
	}

	lines := []string{
		"## Steady-state monitoring dashboard (advisory)",
		"",
		advisoryBanner,
		"",
		fmt.Sprintf("**%d row(s)** across **%d policy version(s)** (%s) · **%d unsafe auto-update(s)** (%s) · abstain-health drift flags: %d · spot-check coverage gaps: %d",
			len(view), len(versions), versionsText, summary.UnsafeAutoUpdatesTotal,
			gateText(summary.Healthy), len(summary.AbstainHealth.DriftFlags),
			len(summary.SpotCheckCoverage.Gaps)),
		"",
		"### Rows (repository × policy version)",
		"",
		"| Repo | Policy | Runs | Decisions | Abstain rate | Override rate | Spot-check sampled/checked | Mispredictions | FP rate | FN rate | Unsafe | Cost |",
		"| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
	}

	cappedRows := view
	if len(cappedRows) > SteadyStateRenderCap {
		cappedRows = cappedRows[:SteadyStateRenderCap]
	}
	for _, row := range cappedRows {
		cost := 0.0
		for _, group := range row.CostAttribution {
			cost += group.TotalCostUsd
		}
		lines = append(lines, fmt.Sprintf(
			"| `%s` | v%d | %d | %d | %s | %s | %d/%d | %d | %s | %s | %d | %s |",
			cell(row.Repo), row.PolicyVersion, row.RunCount, row.DecisionCount,
			formatRate(row.AbstainHealth.AbstainRate), formatRate(row.AbstainHealth.OverrideRate),
			row.SpotCheck.Sampled, row.SpotCheck.Checked, row.SpotCheck.Mispredictions,
			formatRate(row.FalsePositiveRate), formatRate(row.FalseNegativeRate),
			row.UnsafeAutoUpdates, formatCost(cost)))
	}
	rowOverflow := len(view) - len(cappedRows)
	if rowOverflow > 0 {
		lines = append(lines, fmt.Sprintf(
			"_… %d more row(s) omitted (flood cap %d, INV-11) — full set in the committed run-history store._",
			rowOverflow, SteadyStateRenderCap))
	}

	lines = append(lines, "", "### Cost attribution by policy version × model binding", "")
	if len(summary.CostAttribution) == 0 {
		lines = append(lines, "_No usage was attributed — no routed-model traffic in this window._")
	} else {
		lines = append(lines,
			"| Policy | Role | Model | Provider | Usage records | Runs | Tokens | Cost |",
			"| --- | --- | --- | --- | --- | --- | --- | --- |")
		cappedGroups := summary.CostAttribution
		if len(cappedGroups) > SteadyStateRenderCap {
			cappedGroups = cappedGroups[:SteadyStateRenderCap]
		}
		for _, group := range cappedGroups {
			providerText := "—"
			if group.Provider != nil {
				providerText = fmt.Sprintf("`%s`", cell(*group.Provider))
			}
			lines = append(lines, fmt.Sprintf(
				"| v%d | %s | `%s` | %s | %d | %d | %d | %s |",
				group.PolicyVersion, cell(group.Role), cell(group.Model), providerText,
				group.UsageCount, len(group.RunIDs), group.TotalTokens,
				formatCost(group.TotalCostUsd)))
		}
		groupOverflow := len(summary.CostAttribution) - len(cappedGroups)
		if groupOverflow > 0 {
			lines = append(lines, fmt.Sprintf(
				"_… %d more attribution row(s) omitted (flood cap %d, INV-11) — full set in the committed run-history store._",
				groupOverflow, SteadyStateRenderCap))
		}
	}

	lines = append(lines, "", "### Monitors (advisory)")
	band := summary.AbstainHealth.Band
	bandText := "not configured (caller-supplied data, D11)"
	if band != nil {
		minText := "-∞"
		if band.MinAbstainRate != nil {
			minText = jsNumber(*band.MinAbstainRate)
		}
		maxText := "+∞"
		if band.MaxAbstainRate != nil {
			maxText = jsNumber(*band.MaxAbstainRate)
		}
		bandText = fmt.Sprintf("[%s, %s]", minText, maxText)
	}
	lines = append(lines, fmt.Sprintf(
		"- **Abstain-health (R11):** band %s · %d drift flag(s) · %d Phase-10 2-D alert(s).",
		bandText, len(summary.AbstainHealth.DriftFlags), len(summary.AbstainHealth.Alerts)))
	lines = append(lines, fmt.Sprintf(
		"- **Spot-check coverage (R13, OD-R25):** %d audited of %d sampled over %d automatically-applied patch(es) · %d zero-spot-check row(s) flagged (selection-bias guard).",
		summary.SpotCheckCoverage.Checked, summary.SpotCheckCoverage.Sampled,
		summary.SpotCheckCoverage.AutoApplied, len(summary.SpotCheckCoverage.Gaps)))
	if summary.Healthy {
		lines = append(lines, fmt.Sprintf(
			"- **Unsafe auto-updates (ARCH §8.6 gate floor):** %d total — floor met.",
			summary.UnsafeAutoUpdatesTotal))
	} else {
		breached := make([]string, 0, len(summary.UnsafeRows))
		for _, key := range summary.UnsafeRows {
			breached = append(breached, fmt.Sprintf("`%s`", key))
		}
		lines = append(lines, fmt.Sprintf(
			"- **Unsafe auto-updates (ARCH §8.6 gate floor):** %d total — FLOOR BREACHED by %s.",
			summary.UnsafeAutoUpdatesTotal, strings.Join(breached, ", ")))
	}

	lines = append(lines, "", fmt.Sprintf(
		"_Provenance: deterministic render of %d steady-state row(s) · render cap %d (INV-11) · "+
			"P12-T19 (ARCH §5.2, §9, §10.6; R11, R13). No clock is consulted — the same rows "+
			"render byte-identical output._", len(view), SteadyStateRenderCap))
	return strings.Join(lines, "\n")
}

// gateText renders the ARCH §8.6 gate verdict text.
func gateText(healthy bool) string {
	if healthy {
		return "gate floor met"
	}
	return "GATE FLOOR BREACHED — ARCH §8.6"
}
