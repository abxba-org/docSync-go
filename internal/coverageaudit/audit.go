// Ported from core/src/coverage-audit/audit.ts (P11-T22; ARCH §16.1 A5; D14):
// the A5 behavioral-proxy coverage audit engine.
//
// ARCH §16.1 A5 assumes "externally observable behavior changed" is
// deterministically approximable by signature/type/API-surface proxies; this
// engine MEASURES the approximation quality instead of assuming it. Over a
// labeled fixture corpus (each case DECLARES its deterministic ground truth —
// no model judgment), every change is classified into
//
//	(a) the behavioral truth label (behavioral / non-behavioral / uncertain —
//	    declared by the fixture), and
//	(b) whether any deterministic proxy fired (signature / type / visibility /
//	    api-surface / config — the measured record, injected by the caller),
//
// and per-language metrics are computed: recall = proxy-fired /
// behaviorally-relevant, plus the false-positive rate, plus the DECLARED recall
// gap (a behavioral case no proxy detected — recorded in the report, NEVER
// silent, D14) and REVIEW routing for uncertain cases (D14 — ambiguity degrades
// toward less automation, never more).
//
// Deterministic, offline, zero LLM. No clock: at is caller-owned. Output is
// validated against docsync.behavioral-proxy-coverage-report/v1 before return
// — a shape bug fails closed rather than emitting an invalid report.
package coverageaudit

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// BehavioralProxyCoverageCaseInput is one audited corpus case: the declared
// truth label plus the measured proxies (the narrow port).
type BehavioralProxyCoverageCaseInput struct {
	// CaseID is the stable corpus case id.
	CaseID string
	// Language is the language key grouping the per-language rows (e.g.
	// `typescript`, `yaml`).
	Language string
	// Truth is the deterministic ground-truth label declared by the fixture
	// corpus.
	Truth schemas.BehavioralProxyTruth
	// FiredProxies are the proxy kinds that fired for this case (measured
	// upstream — the analyzer or a core classifier). Unknown kinds fail
	// closed (schema validation).
	FiredProxies []string
	// Note is an optional deterministic note carried onto declared-gap
	// records.
	Note *string
}

// BehavioralProxyCoverageAuditInput is the input to RunBehavioralProxyCoverageAudit.
type BehavioralProxyCoverageAuditInput struct {
	// CorpusID identifies the labeled corpus the audit runs over.
	CorpusID string
	// At is the ISO-8601 report stamp (caller-owned; no hidden clock).
	At string
	// RecallFloor is the minimum acceptable per-language recall. Caller
	// supplied — thresholds are calibrated data, never hard-coded
	// (D11-adjacent). A language below the floor fails the audit even with
	// its gaps declared.
	RecallFloor float64
	// DataSource is the provenance of the corpus: 'fixture' (pilot) or
	// 'repository' (evidence). Nil ⇒ the 'fixture' default.
	DataSource *schemas.AuditDataSource
	// Cases are the audited cases (truth × fired proxies).
	Cases []BehavioralProxyCoverageCaseInput
}

// dataSourceDefault mirrors DATA_SOURCE_DEFAULT ('fixture').
var dataSourceDefault = schemas.AuditDataSourceFixture

// BehavioralProxyKinds lists every proxy kind this audit recognizes (mirrors
// the schema enum) — BEHAVIORAL_PROXY_KINDS in the TS barrel.
var BehavioralProxyKinds = schemas.ProxyKindValues

// jsNumber renders a float the way JavaScript's Number#toString / template
// interpolation does, so preserved error and verdict texts read identically
// (1.5 → "1.5", 1 → "1", NaN → "NaN").
func jsNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// compareStrings is the deterministic, locale-independent string order
// (repo convention).
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

// outcomeOf is the deterministic per-case outcome rule (truth × proxy-fired):
// uncertain → review (never scored); behavioral+fired → hit;
// behavioral+missed → declared gap; non-behavioral+fired → false positive;
// non-behavioral+missed → true non-behavioral.
func outcomeOf(truth schemas.BehavioralProxyTruth, proxyFired bool) schemas.AuditOutcome {
	switch truth {
	case schemas.ProxyTruthUncertain:
		return schemas.AuditOutcomeReview
	case schemas.ProxyTruthBehavioral:
		if proxyFired {
			return schemas.AuditOutcomeProxyHit
		}
		return schemas.AuditOutcomeProxyMiss
	default:
		if proxyFired {
			return schemas.AuditOutcomeFalsePositive
		}
		return schemas.AuditOutcomeTrueNonBehavioral
	}
}

// declaredGapDetail renders the declared-gap detail line (note carried when
// the corpus declared one).
func declaredGapDetail(caseID string, note *string) string {
	base := "behaviorally-relevant case with NO behavioral proxy fired — declared recall gap (D14, never silent)"
	if note == nil {
		return fmt.Sprintf("%s [%s]", base, caseID)
	}
	return fmt.Sprintf("%s [%s]: %s", base, caseID, *note)
}

// RunBehavioralProxyCoverageAudit computes the A5 behavioral-proxy coverage
// audit over the given cases. Pure and deterministic: identical inputs yield
// deep-equal reports (cases and rows in code-unit sorted order). Returns an
// error when the computed report fails its own schema (fail closed) — e.g. an
// unknown proxy kind or an out-of-range recall floor.
func RunBehavioralProxyCoverageAudit(input BehavioralProxyCoverageAuditInput) (*schemas.BehavioralProxyCoverageReport, error) {
	if input.RecallFloor < 0 || input.RecallFloor > 1 || math.IsNaN(input.RecallFloor) {
		return nil, fmt.Errorf("behavioral-proxy coverage audit: recallFloor must be within [0, 1], got %s", jsNumber(input.RecallFloor))
	}
	dataSource := dataSourceDefault
	if input.DataSource != nil {
		dataSource = *input.DataSource
	}

	sorted := make([]BehavioralProxyCoverageCaseInput, len(input.Cases))
	copy(sorted, input.Cases)
	sort.SliceStable(sorted, func(i, j int) bool {
		return compareStrings(sorted[i].CaseID, sorted[j].CaseID) < 0
	})

	caseRecords := make([]schemas.AuditCaseRecord, 0, len(sorted))
	for _, entry := range sorted {
		firedProxies := make([]string, len(entry.FiredProxies))
		copy(firedProxies, entry.FiredProxies)
		sort.SliceStable(firedProxies, func(i, j int) bool {
			return compareStrings(firedProxies[i], firedProxies[j]) < 0
		})
		// Fail closed on an unrecognized proxy kind — an unknown measurement
		// is a bug, not a proxy hit (D14; the final schema validation below
		// re-asserts it).
		for _, kind := range firedProxies {
			if !schemas.IsValidProxyKind(kind) {
				return nil, fmt.Errorf("behavioral-proxy coverage audit: case '%s' fired unknown proxy kind '%s'", entry.CaseID, kind)
			}
		}
		proxyFired := len(firedProxies) > 0
		kinds := make([]schemas.ProxyKind, 0, len(firedProxies))
		for _, kind := range firedProxies {
			kinds = append(kinds, schemas.ProxyKind(kind))
		}
		caseRecords = append(caseRecords, schemas.AuditCaseRecord{
			CaseID:       entry.CaseID,
			Language:     entry.Language,
			Truth:        entry.Truth,
			ProxyFired:   proxyFired,
			FiredProxies: kinds,
			Outcome:      outcomeOf(entry.Truth, proxyFired),
			ReviewRouted: entry.Truth == schemas.ProxyTruthUncertain,
		})
	}

	languageSet := map[string]bool{}
	for _, entry := range sorted {
		languageSet[entry.Language] = true
	}
	languages := make([]string, 0, len(languageSet))
	for language := range languageSet {
		languages = append(languages, language)
	}
	sort.SliceStable(languages, func(i, j int) bool {
		return compareStrings(languages[i], languages[j]) < 0
	})

	rows := make([]schemas.AuditLanguageRow, 0, len(languages))
	for _, language := range languages {
		var records []schemas.AuditCaseRecord
		for _, record := range caseRecords {
			if record.Language == language {
				records = append(records, record)
			}
		}
		behaviorRelevant := 0
		proxyHits := 0
		falsePositives := 0
		declaredGaps := []schemas.AuditDeclaredGap{}
		reviewRouted := []string{}
		for _, record := range records {
			switch record.Truth {
			case schemas.ProxyTruthBehavioral:
				behaviorRelevant++
				if record.ProxyFired {
					proxyHits++
				}
			}
			if record.Outcome == schemas.AuditOutcomeFalsePositive {
				falsePositives++
			}
			if record.Outcome == schemas.AuditOutcomeProxyMiss {
				var note *string
				for _, entry := range sorted {
					if entry.CaseID == record.CaseID {
						note = entry.Note
						break
					}
				}
				declaredGaps = append(declaredGaps, schemas.AuditDeclaredGap{
					CaseID: record.CaseID,
					Detail: declaredGapDetail(record.CaseID, note),
				})
			}
			if record.ReviewRouted {
				reviewRouted = append(reviewRouted, record.CaseID)
			}
		}
		// Vacuously 1 when the language has no behaviorally-relevant cases:
		// nothing behaviorally relevant can be missed, so there is no gap.
		recall := 1.0
		if behaviorRelevant > 0 {
			recall = float64(proxyHits) / float64(behaviorRelevant)
		}
		falsePositiveRate := 0.0
		if len(records) > 0 {
			falsePositiveRate = float64(falsePositives) / float64(len(records))
		}
		rows = append(rows, schemas.AuditLanguageRow{
			Language:              language,
			AnalyzedCases:         len(records),
			BehaviorRelevantCases: behaviorRelevant,
			ProxyHits:             proxyHits,
			Recall:                recall,
			FalsePositives:        falsePositives,
			FalsePositiveRate:     falsePositiveRate,
			DeclaredGaps:          declaredGaps,
			ReviewRouted:          reviewRouted,
		})
	}

	summary := schemas.AuditSummary{}
	for _, record := range caseRecords {
		summary.Cases++
		if record.Truth == schemas.ProxyTruthBehavioral {
			summary.BehaviorRelevant++
		}
		switch record.Outcome {
		case schemas.AuditOutcomeProxyHit:
			summary.ProxyHits++
		case schemas.AuditOutcomeFalsePositive:
			summary.FalsePositives++
		case schemas.AuditOutcomeProxyMiss:
			summary.DeclaredGaps++
		}
		if record.ReviewRouted {
			summary.ReviewRouted++
		}
	}

	minRecall := math.NaN()
	for _, row := range rows {
		if math.IsNaN(minRecall) || row.Recall < minRecall {
			minRecall = row.Recall
		}
	}
	minRecallText := jsNumber(1)
	if !math.IsNaN(minRecall) {
		minRecallText = jsNumber(minRecall)
	}

	var belowFloor []schemas.AuditLanguageRow
	for _, row := range rows {
		if row.Recall < input.RecallFloor {
			belowFloor = append(belowFloor, row)
		}
	}

	var verdict schemas.AuditVerdict
	var verdictReason string
	switch {
	case len(belowFloor) > 0:
		verdict = schemas.AuditVerdictFail
		parts := make([]string, 0, len(belowFloor))
		for _, row := range belowFloor {
			parts = append(parts, fmt.Sprintf("%s=%s", row.Language, jsNumber(row.Recall)))
		}
		verdictReason = fmt.Sprintf("language recall below the recall floor %s: %s",
			jsNumber(input.RecallFloor), joinComma(parts))
	case summary.DeclaredGaps == 0:
		verdict = schemas.AuditVerdictPass
		verdictReason = fmt.Sprintf("all language recalls (min %s) meet the recall floor %s with no declared gaps",
			minRecallText, jsNumber(input.RecallFloor))
	default:
		verdict = schemas.AuditVerdictPassWithDeclaredGaps
		verdictReason = fmt.Sprintf("all language recalls (min %s) meet the recall floor %s with %d declared recall gap(s) — declared, never silent (D14)",
			minRecallText, jsNumber(input.RecallFloor), summary.DeclaredGaps)
	}

	report := &schemas.BehavioralProxyCoverageReport{
		Schema:        "docsync.behavioral-proxy-coverage-report/v1",
		CorpusID:      input.CorpusID,
		At:            input.At,
		RecallFloor:   input.RecallFloor,
		DataSource:    dataSource,
		Languages:     rows,
		Cases:         caseRecords,
		Summary:       summary,
		Verdict:       verdict,
		VerdictReason: verdictReason,
	}
	// Fail closed on any shape drift (unknown proxy kind, bad label, …).
	if issues := report.Validate(); len(issues) > 0 {
		return nil, schemas.NewValidationError(issues...)
	}
	return report, nil
}

// joinComma renders ", " between parts (Array#join semantics).
func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}
