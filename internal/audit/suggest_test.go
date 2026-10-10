// Black-box port of packages/core/test/audit-suggest.test.ts (P11-T21; ARCH
// §7.2/§10.2; PLAN §12.5 V-P11-10).
//
// Pins:
//   - P11-T21 candidates carry EVIDENCE ONLY (the closed key set has no
//     outcome member), floor-mode findings stay advisory, unknown modes fail
//     closed to advisory, and the flow passes the entry's outcome through
//     VERBATIM (no outcome re-derivation in the drift path).
//   - D7: candidates outside the manifest never enter the run.
//   - V-P11-10 identity surface: the flow uses the injected shared entry
//     INSTANCE (one instance serves both triggers). The binding of that
//     instance to the real orchestrator entry is the W5b2 wiring wave's job —
//     internal/orchestrator is not landed yet (see seams.go), so the identity
//     is asserted here on the injection seam the wiring consumes.
//
// The fixture-driven half of the TS suite (a suggest-mode finding produced by
// the composed nightly audit) is ported in compose_test.go, which feeds the
// real audit's findings through this same mapping.

package audit_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DUDE616/docSync-go/internal/audit"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
	"github.com/stretchr/testify/require"
)

// suggestFinding is the TS pure-mapping finding factory.
func suggestFinding(id, docPath string, claimID *string) audit.DriftFinding {
	finding := audit.DriftFinding{
		ID:      id,
		ClaimID: claimID,
		DocPath: docPath,
		Kind:    "claim-lost-support",
		Evidence: audit.DriftFindingEvidence{
			Commits: []string{"c1"},
			Paths:   []string{"src/x.ts"},
			Detail:  id + " lost support",
		},
		CreatedAt: auditNow,
	}
	return finding
}

// candidateKeys returns the sorted wire keys of a marshaled candidate (the TS
// `Object.keys(candidate).sort()` assertion).
func candidateKeys(t *testing.T, candidate audit.DriftSuggestCandidate) []string {
	t.Helper()
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	var wire map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &wire))
	keys := make([]string, 0, len(wire))
	for key := range wire {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func TestP11T21_CandidatesCarryEvidenceOnly(t *testing.T) {
	findings := []audit.DriftFinding{
		suggestFinding("f-1", "docs/a.md", strPtr("claim_1")),
		suggestFinding("f-2", "docs/locked.md", nil),
	}

	mapping := audit.FindingsToSuggestCandidates(findings, map[string]workspace.WorkspaceMode{
		"docs/a.md":      "suggest",
		"docs/locked.md": "locked",
	})

	// The suggest-mode doc yields one evidence-only candidate...
	require.Len(t, mapping.Candidates, 1)
	candidate := mapping.Candidates[0]
	// ...with NO outcome member: the closed key set carries evidence only.
	require.Equal(t, []string{
		"claimId", "commits", "docPath", "findingId", "kind", "mode", "paths", "reason", "trigger",
	}, candidateKeys(t, candidate))
	require.NotContains(t, candidateKeys(t, candidate), "outcome")
	require.NotContains(t, candidateKeys(t, candidate), "policy_outcome")
	require.Equal(t, audit.DriftSuggestTriggerLoopB, candidate.Trigger)
	require.NotNil(t, candidate.ClaimID)
	require.Equal(t, "claim_1", *candidate.ClaimID)
	require.Equal(t, "f-1 lost support", candidate.Reason)
	require.Equal(t, "claim-lost-support", candidate.Kind)
	require.Equal(t, []string{"c1"}, candidate.Commits)

	// The floor-mode doc NEVER becomes a patch candidate — advisory only.
	require.Len(t, mapping.AdvisoryOnly, 1)
	require.Equal(t, audit.DriftAdvisoryOnly{
		DocPath:   "docs/locked.md",
		FindingID: "f-2",
		Mode:      workspaceModePtr("locked"),
		Reason:    `ownership-floor mode "locked" would resolve blocked at any confidence (D2) — advisory only`,
	}, mapping.AdvisoryOnly[0])

	// Unknown/absent mode: fail closed to advisory (D7 external posture).
	unmapped := audit.FindingsToSuggestCandidates(findings, nil)
	require.Empty(t, unmapped.Candidates)
	require.Len(t, unmapped.AdvisoryOnly, 2)
	require.Nil(t, unmapped.AdvisoryOnly[0].Mode)
	require.Nil(t, unmapped.AdvisoryOnly[1].Mode)
	require.Contains(t, unmapped.AdvisoryOnly[0].Reason, "fail closed to advisory")

	// The mapping is deterministic: candidates sorted by (docPath, findingId).
	require.Equal(t, "docs/a.md", mapping.Candidates[0].DocPath)
	require.Equal(t, "docs/locked.md", mapping.AdvisoryOnly[0].DocPath)
}

func TestP11T21_FlowInvokesTheSharedEntryOnlyForInScopeCandidates(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/a.md", Mode: modePtr(schemas.DocModeSuggest), Sources: []string{"src/**"}},
		},
	}
	input := &audit.PipelineInput{
		Repo:     "drift-fixture",
		RepoRoot: "/unused",
		Base:     "base",
		Head:     "head",
		Manifest: &manifest,
		Policy:   map[string]any{"policy_version": 1},
		Clock:    func() string { return auditNow },
	}
	candidate := audit.DriftSuggestCandidate{
		DocPath:   "docs/a.md",
		Mode:      "suggest",
		ClaimID:   nil,
		FindingID: "f-1",
		Kind:      "doc-drift",
		Reason:    "document drifted from its baseline",
		Commits:   []string{"c1"},
		Paths:     []string{"docs/a.md"},
		Trigger:   audit.DriftSuggestTriggerLoopB,
	}
	outOfScope := candidate
	outOfScope.DocPath = "docs/unlisted.md"

	// The marker outcome: the drift path passes the entry's result through
	// VERBATIM — it re-derives NO outcome vocabulary of its own.
	marker := &schemas.RunOutcome{RunID: "marker-run"}
	entryCalls := 0
	stubEntry := func(context.Context, *audit.PipelineInput, audit.PipelinePorts) (*schemas.RunOutcome, error) {
		entryCalls++
		return marker, nil
	}

	// Empty candidates: nothing is fed, nothing runs — and the entry identity
	// is reported verbatim.
	empty, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates:    nil,
		Input:         input,
		PipelineEntry: stubEntry,
	})
	require.NoError(t, err)
	require.Equal(t, audit.DriftSuggestTriggerLoopB, empty.Trigger)
	require.Empty(t, empty.Candidates)
	require.Nil(t, empty.Outcome)
	require.NotNil(t, empty.PipelineEntry)
	require.Equal(t, 0, entryCalls)

	// Only out-of-scope candidates (D7): the entry is never invoked.
	allUnlisted, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates:    []audit.DriftSuggestCandidate{outOfScope},
		Input:         input,
		PipelineEntry: stubEntry,
	})
	require.NoError(t, err)
	require.Nil(t, allUnlisted.Outcome)
	require.Equal(t, 0, entryCalls)

	// In-scope candidate: exactly ONE entry invocation, outcome verbatim.
	ran, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates:    []audit.DriftSuggestCandidate{candidate, outOfScope},
		Input:         input,
		PipelineEntry: stubEntry,
	})
	require.NoError(t, err)
	require.Equal(t, 1, entryCalls)
	require.Equal(t, audit.DriftSuggestTriggerLoopB, ran.Trigger)
	require.Same(t, marker, ran.Outcome)
	require.Len(t, ran.Candidates, 2)
}

func TestP11T21_NilEntryInvokesNothingFailClosed(t *testing.T) {
	manifest := schemas.Manifest{
		Version: 1,
		Documents: []schemas.ManifestEntry{
			{Path: "docs/a.md", Sources: []string{"src/**"}},
		},
	}
	input := &audit.PipelineInput{Manifest: &manifest}
	result, err := audit.RunDriftSuggestFlow(context.Background(), audit.DriftSuggestFlowDeps{
		Candidates: []audit.DriftSuggestCandidate{{
			DocPath: "docs/a.md", Mode: "suggest", FindingID: "f-1", Trigger: audit.DriftSuggestTriggerLoopB,
		}},
		Input: input,
	})
	require.NoError(t, err)
	// internal/orchestrator is not landed: an unbound entry invokes NOTHING
	// (fail closed) rather than running a second, divergent engine.
	require.Nil(t, result.Outcome)
	require.Nil(t, result.PipelineEntry)
}

// workspaceModePtr returns a pointer to a mode (optional-field helper).
func workspaceModePtr(mode workspace.WorkspaceMode) *workspace.WorkspaceMode {
	return &mode
}

// modePtr returns a pointer to a manifest doc mode.
func modePtr(mode schemas.DocMode) *schemas.DocMode { return &mode }
