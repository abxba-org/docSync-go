// Ported from core/src/audit/dedup.ts (P11-T19; ARCH §7.2: "findings
// deduplicated by claim ID so nightly runs don't spam"). PURE: findings in,
// deduplicated findings out — no IO, no clock.
//
// PINNED TIE-BREAK RULE (decided in the TS module, pinned by its tests):
//
//   - A finding WITH a claim_id dedupes on `claim:<claim_id>` — the claim ID
//     is the identity, regardless of which document produced the occurrence.
//   - A finding WITHOUT a claim_id dedupes on `doc:<doc_path>|kind:<kind>`.
//   - When several occurrences share a key, ONE finding survives: the LAST
//     occurrence in scan order (chunk completion order — deterministic given
//     the deterministic manifest chunk order) wins the scalar body (`id`,
//     `doc_path`, `kind`, `confidence`, `created_at`, `evidence.detail`);
//     `evidence.commits` and `evidence.paths` MERGE as sorted, de-duplicated
//     unions across all occurrences — so repeated sweeps accumulate commit
//     evidence instead of losing it, while the verdict text stays latest-wins.
//
// Cross-RUN idempotency is the cursor's job (a resumed run does not re-audit
// completed chunks) plus this within-run dedup (a re-audited chunk's repeated
// claim-ID findings collapse) — ARCH §7.2 cost control.
package audit

import "sort"

// FindingDedupKey is the dedup identity of a finding (claim-first; doc+kind
// fallback).
func FindingDedupKey(finding DriftFinding) string {
	if finding.ClaimID != nil {
		return "claim:" + *finding.ClaimID
	}
	return "doc:" + finding.DocPath + "|kind:" + finding.Kind
}

// uniqueSorted ports the TS `[...new Set(values)].sort()`: de-duplicated and
// lexicographically sorted. The result is always non-nil so callers can
// assert on an empty union deterministically.
func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// mergeLatest ports the TS merge: latest-wins scalars + unioned evidence (see
// the module header for the rule).
func mergeLatest(earlier, later DriftFinding) DriftFinding {
	merged := later
	merged.Evidence = DriftFindingEvidence{
		Commits: uniqueSorted(append(append([]string{}, earlier.Evidence.Commits...), later.Evidence.Commits...)),
		Paths:   uniqueSorted(append(append([]string{}, earlier.Evidence.Paths...), later.Evidence.Paths...)),
		Detail:  later.Evidence.Detail,
	}
	return merged
}

// compareFindings is the pinned deterministic ordering: doc_path, kind,
// claim_id, id.
func compareFindings(a, b DriftFinding) int {
	if a.DocPath != b.DocPath {
		return compareStrings(a.DocPath, b.DocPath)
	}
	if a.Kind != b.Kind {
		return compareStrings(a.Kind, b.Kind)
	}
	aClaim := ""
	if a.ClaimID != nil {
		aClaim = *a.ClaimID
	}
	bClaim := ""
	if b.ClaimID != nil {
		bClaim = *b.ClaimID
	}
	if aClaim != bClaim {
		return compareStrings(aClaim, bClaim)
	}
	if a.ID == b.ID {
		return 0
	}
	return compareStrings(a.ID, b.ID)
}

// compareStrings is the locale-independent string comparator.
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

// DedupeFindings deduplicates findings by the pinned rule above and returns
// them sorted deterministically (doc_path, kind, claim_id, id).
func DedupeFindings(findings []DriftFinding) []DriftFinding {
	byKey := make(map[string]DriftFinding, len(findings))
	order := make([]string, 0, len(findings))
	for _, finding := range findings {
		key := FindingDedupKey(finding)
		existing, ok := byKey[key]
		if !ok {
			order = append(order, key)
			byKey[key] = finding
			continue
		}
		byKey[key] = mergeLatest(existing, finding)
	}
	out := make([]DriftFinding, 0, len(byKey))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return compareFindings(out[i], out[j]) < 0 })
	return out
}
