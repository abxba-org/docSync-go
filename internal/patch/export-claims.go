package patch

// Ported from core/src/patch/export-claims.ts — per-patch claim-value export
// (P3-T13; R16 cross-patch consistency): the surface that makes two patches
// asserting DIFFERENT values for the SAME claim id detectable.
//
// ExportPatchClaimValues flattens a DocPatch's hunks into (claim_id, doc_path,
// hunk_index, value=evidence-backed inserted text) records;
// DetectClaimValueConflicts groups them by claim_id and reports every claim id
// whose records carry MORE THAN ONE DISTINCT value (single-value groups are
// consistent and never reported).
//
// Pure, deterministic, dependency-free beyond the schema type. This is the DATA
// surface only: the full cross-patch consistency check (who runs it, when,
// against the whole run's exports) lands with the verification engine in Phase 4
// (R16's binding surface is the shared claim id, the group shape here).

import (
	"sort"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ExportedClaimValue (TS: ExportedClaimValue) is one hunk-level claim assertion
// exported from a DocPatch (R16).
type ExportedClaimValue struct {
	ClaimID   string `json:"claim_id"`
	DocPath   string `json:"doc_path"`
	HunkIndex int    `json:"hunk_index"`
	// Value is the asserted value — the hunk's inserted_text ('' for
	// deletion-only hunks).
	Value string `json:"value"`
	// EvidenceRefKinds are the kinds of the hunk's machine-resolvable evidence
	// refs (OD-R08 surface).
	EvidenceRefKinds []string `json:"evidence_ref_kinds"`
}

// ClaimValueRecord is one {doc_path, value} record inside a conflict group
// (TS: the inline {doc_path, value} object).
type ClaimValueRecord struct {
	DocPath string `json:"doc_path"`
	Value   string `json:"value"`
}

// ClaimValueConflict (TS: ClaimValueConflict) is one claim id's conflict record:
// the distinct-value groups sharing that id.
type ClaimValueConflict struct {
	ClaimID string `json:"claim_id"`
	// Conflicts is one inner slice per DISTINCT value (sorted by value), each
	// carrying every {doc_path, value} record asserting that value (sorted by
	// doc_path). Two or more inner groups = the conflict.
	Conflicts [][]ClaimValueRecord `json:"conflicts"`
}

// ExportPatchClaimValues (TS: exportPatchClaimValues) flattens a DocPatch into
// claim-value records (R16). Hunks WITHOUT claim_ids export nothing (per-hunk
// evidence refs remain the mandatory minimum per OD-R08 — the claim layer is the
// stronger, optional provenance). One record per (hunk, claim_id) pair; order
// follows hunk order, then claim_ids order (deterministic).
func ExportPatchClaimValues(patch *schemas.DocPatch) []ExportedClaimValue {
	exported := []ExportedClaimValue{}
	for hunkIndex := range patch.Hunks {
		hunk := &patch.Hunks[hunkIndex]
		for _, claimID := range hunk.ClaimIDs {
			kinds := make([]string, len(hunk.EvidenceRefs))
			for i := range hunk.EvidenceRefs {
				kinds[i] = string(hunk.EvidenceRefs[i].Kind)
			}
			exported = append(exported, ExportedClaimValue{
				ClaimID:          claimID,
				DocPath:          patch.DocPath,
				HunkIndex:        hunkIndex,
				Value:            hunk.InsertText,
				EvidenceRefKinds: kinds,
			})
		}
	}
	return exported
}

// DetectClaimValueConflicts (TS: detectClaimValueConflicts) detects cross-patch
// claim-value conflicts (R16): groups the exports by claim_id, then by value; a
// claim id with ≥ 2 distinct values yields one ClaimValueConflict.
// Deterministic: output sorted by claim_id; inner groups sorted by value; records
// within a group sorted by doc_path. Pure.
func DetectClaimValueConflicts(exports []ExportedClaimValue) []ClaimValueConflict {
	byClaim := make(map[string][]ExportedClaimValue)
	var claimOrder []string
	for _, record := range exports {
		if _, ok := byClaim[record.ClaimID]; !ok {
			claimOrder = append(claimOrder, record.ClaimID)
		}
		byClaim[record.ClaimID] = append(byClaim[record.ClaimID], record)
	}
	sort.Strings(claimOrder)

	conflicts := []ClaimValueConflict{}
	for _, claimID := range claimOrder {
		records := byClaim[claimID]
		byValue := make(map[string][]ClaimValueRecord)
		var valueOrder []string
		for _, record := range records {
			rec := ClaimValueRecord{DocPath: record.DocPath, Value: record.Value}
			if _, ok := byValue[record.Value]; !ok {
				valueOrder = append(valueOrder, record.Value)
			}
			byValue[record.Value] = append(byValue[record.Value], rec)
		}
		if len(byValue) < 2 {
			continue // single-value group → consistent
		}
		sort.Strings(valueOrder)
		var groups [][]ClaimValueRecord
		for _, value := range valueOrder {
			recs := byValue[value]
			sort.SliceStable(recs, func(i, j int) bool {
				return recs[i].DocPath < recs[j].DocPath
			})
			groups = append(groups, recs)
		}
		conflicts = append(conflicts, ClaimValueConflict{ClaimID: claimID, Conflicts: groups})
	}
	return conflicts
}
