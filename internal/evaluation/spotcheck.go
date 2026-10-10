// Ported from core/src/evaluation/spotcheck.ts (P10-T11; ARCH §9; CTX §35;
// V-P10-9/R13; OD-R25): the spot-check sampler + audit-finding ingestion.
//
// Auto-applied patches are the riskiest write path, so a deterministic, seeded
// sample of every auto-apply batch is selected for human audit; the audit
// verdicts re-enter the evaluation loop as hygiene-label-shaped rows
// (`source_signal: 'audit_finding'`) — eval labels, never side-channel evidence.
//
// OD-R25 (adopted default): SPOTCHECK_RATE = 0.1 (10%), with a floor of
// SPOTCHECK_MIN_PER_50 = 1 per 50 auto-applied patches — both exported as
// versioned data constants (D11-adjacent: thresholds are data, declared and
// exported, never silently hard-coded). Selection is driven by the caller's
// seeded PRNG — the mulberry32 generator from the splits module (`Math.random`
// appears nowhere); the same seed selects the exact same rows forever.
//
// Fail closed: audit findings that cannot yield a schema-valid hygiene label
// are refused with an explicit error — a malformed audit verdict never lands
// in the ground-truth label stream.
package evaluation

import (
	"crypto/rand"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// SpotCheckRate is the spot-check sampling rate (OD-R25 adopted default,
// versioned data): 10% of every auto-apply batch, deterministically selected.
const SpotCheckRate = 0.1

// SpotCheckMinPer50 is the spot-check floor (OD-R25, versioned data): at least
// one sampled patch per 50 auto-applied patches, on top of the rate-based
// share.
const SpotCheckMinPer50 = 1

// SpotCheckSampleFloor is the absolute per-batch sampling floor — a non-empty
// batch is never unaudited.
const SpotCheckSampleFloor = 1

// SpotCheckFloorWindow is the determinism window size for the SpotCheckMinPer50
// floor.
const SpotCheckFloorWindow = 50

// AutoAppliedPatch is one auto-applied patch row — identified by its decision
// id.
type AutoAppliedPatch struct {
	// DecisionID is the decision id the patch was applied under (spot-check
	// identity).
	DecisionID string
}

// SpotCheckSampleSize computes the spot-check sample size for a batch of
// batchSize auto-applied patches: `round(batchSize * SPOTCHECK_RATE)`, raised
// to at least `floor(batchSize / SPOTCHECK_FLOOR_WINDOW) * SPOTCHECK_MIN_PER_50`
// and to SpotCheckSampleFloor — a non-empty batch always yields at least one
// audited patch (fail closed: never audit nothing).
func SpotCheckSampleSize(batchSize int) (int, error) {
	if batchSize < 0 {
		return 0, fmt.Errorf("spotCheckSampleSize: batchSize must be a non-negative integer, got %d", batchSize)
	}
	if batchSize == 0 {
		return 0, nil
	}
	byRate := int(math.Round(float64(batchSize) * SpotCheckRate))
	byFloor := (batchSize / SpotCheckFloorWindow) * SpotCheckMinPer50
	size := SpotCheckSampleFloor
	if byRate > size {
		size = byRate
	}
	if byFloor > size {
		size = byFloor
	}
	return size, nil
}

// SampleAutoPatches deterministically selects the spot-check sample of one
// auto-apply batch: stable sort by decision id, then a seeded Fisher-Yates
// shuffle picks SpotCheckSampleSize rows; the selected rows are returned sorted
// by decision id. Same batch + same seed ⇒ byte-identical selection (V-P10-9).
func SampleAutoPatches(autoApplied []AutoAppliedPatch, rng func() float64) ([]AutoAppliedPatch, error) {
	if len(autoApplied) == 0 {
		return []AutoAppliedPatch{}, nil
	}
	for _, value := range autoApplied {
		if value.DecisionID == "" {
			return nil, fmt.Errorf("sampleAutoPatches: every patch needs a non-empty decisionId")
		}
	}
	ordered := make([]AutoAppliedPatch, len(autoApplied))
	copy(ordered, autoApplied)
	sort.SliceStable(ordered, func(i, j int) bool {
		return compareStrings(ordered[i].DecisionID, ordered[j].DecisionID) < 0
	})
	indices := make([]int, len(ordered))
	for i := range indices {
		indices[i] = i
	}
	for i := len(indices) - 1; i > 0; i-- {
		j := int(math.Floor(rng() * float64(i+1)))
		indices[i], indices[j] = indices[j], indices[i]
	}
	count, err := SpotCheckSampleSize(len(ordered))
	if err != nil {
		return nil, err
	}
	if count > len(ordered) {
		count = len(ordered)
	}
	selected := make([]AutoAppliedPatch, 0, count)
	for _, index := range indices[:count] {
		selected = append(selected, ordered[index])
	}
	sort.SliceStable(selected, func(i, j int) bool {
		return compareStrings(selected[i].DecisionID, selected[j].DecisionID) < 0
	})
	return selected, nil
}

// AuditVerdict is the closed audit-verdict union a spot-check audit can
// produce.
type AuditVerdict string

// The audit verdicts in canonical order.
const (
	AuditVerdictPatchCorrect      AuditVerdict = "patch_correct"
	AuditVerdictPatchIncorrect    AuditVerdict = "patch_incorrect"
	AuditVerdictUpdateUnwarranted AuditVerdict = "update_unwarranted"
)

// AuditFindingSourceSignal is the hygiene-label source signal every ingested
// audit row carries.
const AuditFindingSourceSignal = "audit_finding"

// auditVerdictLabel maps an audit verdict onto (label, hygiene_rule)
// (ARCH §8.7 vocabulary reuse, OD-2): the audit is a reviewer-quality signal,
// so `patch_correct` accepts the patch, `patch_incorrect` marks it corrected
// (it needed fixing), and `update_unwarranted` marks the update itself a false
// positive (the doc was not stale). Rules follow the `<source>:<qualifier>⇒
// <label>` derivation shape and match HYGIENE_RULE_REGEX.
type auditVerdictMapping struct {
	Label       schemas.HygieneLabelName
	HygieneRule string
}

var auditVerdictLabels = map[AuditVerdict]auditVerdictMapping{
	AuditVerdictPatchCorrect: {
		Label:       schemas.HygieneLabelAccepted,
		HygieneRule: "audit_finding:patch_correct⇒accepted",
	},
	AuditVerdictPatchIncorrect: {
		Label:       schemas.HygieneLabelCorrected,
		HygieneRule: "audit_finding:patch_incorrect⇒corrected",
	},
	AuditVerdictUpdateUnwarranted: {
		Label:       schemas.HygieneLabelFalsePositive,
		HygieneRule: "audit_finding:update_unwarranted⇒false_positive",
	},
}

// AuditFinding is one spot-check audit verdict on an auto-applied patch.
type AuditFinding struct {
	// DecisionID is the decision id the audited patch was applied under.
	DecisionID string
	// PolicyVersion is the policy version that produced the audited patch.
	PolicyVersion int
	// Verdict is the auditor's verdict (closed AuditVerdict union).
	Verdict AuditVerdict
	// At is the injected ISO-8601 instant of the audit — never generated here.
	At string
}

// IngestAuditFindingsOptions carries the injected millisecond clock and byte
// source for `label_id` ULIDs (deterministic tests). Nil fields fall back to
// the platform clock / CSPRNG (the TS default path).
type IngestAuditFindingsOptions struct {
	NowMs     *int64
	RandBytes func(n int) []byte
}

// defaultULIDs is the package-level default generator (the ONLY clock reader,
// mirroring the TS module state's default path). Deterministic callers inject
// NowMs + RandBytes instead and get a fresh per-call generator — the same
// effect the TS suite achieved with resetUlidState before a deterministic
// ingestion.
var defaultULIDs = docutil.NewULIDGenerator()

// IngestAuditFindings ingests spot-check audit findings as eval labels: one
// schema-validated hygiene-label row per finding (`source_signal:
// 'audit_finding'`, verdict traceable via `hygiene_rule`), validated against
// HYGIENE_LABEL_SCHEMA — fail closed (an invalid row refuses the batch with an
// explicit error, it is never silently dropped into the ground-truth stream).
func IngestAuditFindings(findings []AuditFinding, opts *IngestAuditFindingsOptions) ([]schemas.HygieneLabel, error) {
	generator := defaultULIDs
	if opts != nil && (opts.NowMs != nil || opts.RandBytes != nil) {
		nowMs := func() int64 { return time.Now().UnixMilli() }
		if opts.NowMs != nil {
			injected := *opts.NowMs
			nowMs = func() int64 { return injected }
		}
		randBytes := opts.RandBytes
		if randBytes == nil {
			randBytes = func(n int) []byte {
				buf := make([]byte, n)
				if _, err := rand.Read(buf); err != nil {
					// Unreachable in practice (crypto/rand on Linux); the
					// generator refuses the row rather than inventing bytes.
					return nil
				}
				return buf
			}
		}
		generator = docutil.NewULIDGeneratorWith(nowMs, randBytes)
	}

	labels := make([]schemas.HygieneLabel, 0, len(findings))
	for index, finding := range findings {
		if finding.DecisionID == "" {
			return nil, fmt.Errorf("ingestAuditFindings: finding %d carries an empty decisionId (fail closed)", index)
		}
		if finding.PolicyVersion < 1 {
			return nil, fmt.Errorf("ingestAuditFindings: finding %d policyVersion must be a positive integer, got %d",
				index, finding.PolicyVersion)
		}
		verdictLabel, ok := auditVerdictLabels[finding.Verdict]
		if !ok {
			return nil, fmt.Errorf("ingestAuditFindings: finding %d carries an unknown verdict: %s",
				index, string(finding.Verdict))
		}
		labelID, err := generator.Generate()
		if err != nil {
			return nil, err
		}
		candidate := schemas.HygieneLabel{
			Schema:        "docsync.hygiene-label/v1",
			LabelID:       labelID,
			DecisionID:    finding.DecisionID,
			PolicyVersion: finding.PolicyVersion,
			Label:         verdictLabel.Label,
			SourceSignal:  schemas.HygieneSourceAuditFinding,
			HygieneRule:   verdictLabel.HygieneRule,
			NormalizedAt:  finding.At,
		}
		// Fail closed: the produced row must round-trip the hygiene-label
		// schema.
		if issues := candidate.Validate(); len(issues) > 0 {
			return nil, fmt.Errorf("ingestAuditFindings: finding %d produced an invalid hygiene label (fail closed): %s",
				index, schemaIssues(issues))
		}
		labels = append(labels, candidate)
	}
	return labels, nil
}
