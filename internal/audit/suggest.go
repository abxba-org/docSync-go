// Ported from core/src/audit/suggest.ts (P11-T21; ARCH §7.2, §10.2). Findings
// → SUGGEST shared path: findings warranting patches become SUGGEST items
// processed by the SAME verification + policy engine as Loop A — ONE CODE
// PATH, TWO TRIGGERS.
//
// Mode-ceiling discipline WITHOUT importing @docsync/policy (core depends on
// schemas only; OD-1 topology — policy does not depend on core either, so no
// import is possible in either direction): candidates carry EVIDENCE ONLY and
// the SHARED policy stage decides the outcome. The only drift-side filter is
// the ownership floor mirrored as core data (workspace.ProtectedModes — the D2
// floor whose policy-side counterpart is `applyOwnershipFloor`): findings on
// docs whose manifest mode resolves to `locked`/`generated`/`external` would
// resolve `blocked` at ANY confidence, so they are NOT turned into patch
// candidates — they stay advisory. Writable modes (`auto`/`suggest`/`human`)
// yield candidates; whether such a candidate ends up suggest/auto/review/
// blocked is decided EXCLUSIVELY by the pipeline's own policy stage — this
// module re-derives NO outcome.
//
// The Loop-A entry is the shared orchestrator entry (orchestrator/run.ts's
// runPipeline — the exact function object the PR path composes through). The
// flow funnels the drift trigger through the INJECTED entry (see seams.go:
// internal/orchestrator is not landed yet, so the entry is a caller-wired
// seam whose identity the wiring wave pins as
// `LoopAPipelineEntry === orchestrator.RunPipeline`, V-P11-10) and records
// `loop-b` on every candidate and on the flow result. The flow adds NO
// parallel engine: the pipeline's own retrieval/writer/verification/policy
// stages do the rest over the caller-composed input + ports (the caller
// derives the change window from the finding evidence — e.g. base = the
// evidence commits' merge-base, head = HEAD — exactly as the W5b2 wiring
// will).
package audit

import (
	"context"
	"fmt"
	"sort"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/workspace"
)

/* --------------------------------------------------------------- candidates */

// DriftSuggestTrigger is the loop-b trigger tag carried by every drift SUGGEST
// candidate.
type DriftSuggestTrigger string

// DriftSuggestTriggerLoopB is the drift trigger tag (always `loop-b`).
const DriftSuggestTriggerLoopB DriftSuggestTrigger = "loop-b"

// DriftSuggestCandidate is ONE evidence-only SUGGEST candidate derived from
// one drift finding (ARCH §7.2: findings warranting patches become SUGGEST
// items). Carries NO outcome: the pipeline's shared policy stage decides
// auto/suggest/review/blocked.
//
// The wire key names mirror the TS object members verbatim (the closed key set
// the TS suite pinned: exactly claimId/commits/docPath/findingId/kind/mode/
// paths/reason/trigger — no outcome member exists).
type DriftSuggestCandidate struct {
	// DocPath is the manifest-scoped document the candidate proposes to
	// investigate.
	DocPath string `json:"docPath"`
	// Mode is the doc's manifest mode as resolved by the caller (D2 floor
	// data).
	Mode workspace.WorkspaceMode `json:"mode"`
	// ClaimID is the finding's claim id (nil for claimless findings).
	ClaimID *string `json:"claimId,omitempty"`
	// FindingID is the source finding's id (provenance).
	FindingID string `json:"findingId"`
	// Kind is the drift-kind tag (closed vocabulary, P11-T20).
	Kind string `json:"kind"`
	// Reason is the finding's evidence detail — the SUGGEST reason.
	Reason string `json:"reason"`
	// Commits are the evidence commits from the finding (the caller derives
	// the window from these).
	Commits []string `json:"commits"`
	// Paths are the evidence paths from the finding.
	Paths []string `json:"paths"`
	// Trigger is the trigger that produced this candidate — always the drift
	// trigger.
	Trigger DriftSuggestTrigger `json:"trigger"`
}

// DriftAdvisoryOnly is a finding deliberately NOT turned into a patch
// candidate (stays advisory).
type DriftAdvisoryOnly struct {
	DocPath   string `json:"docPath"`
	FindingID string `json:"findingId"`
	// Mode is the floor mode that kept it advisory (nil = unknown mode, fail
	// closed).
	Mode *workspace.WorkspaceMode `json:"mode,omitempty"`
	// Reason explains the advisory-only routing.
	Reason string `json:"reason"`
}

// SuggestCandidateMapping is the candidates/advisory-only split.
type SuggestCandidateMapping struct {
	Candidates   []DriftSuggestCandidate
	AdvisoryOnly []DriftAdvisoryOnly
}

// FindingsToSuggestCandidates maps drift findings onto evidence-only SUGGEST
// candidates (see the module doc for the mode-ceiling discipline). `docModes`
// is CALLER-SUPPLIED data (docPath → manifest mode, the same map the
// composition hands the policy stage); a doc missing from the map is the D7
// `external` pseudo-mode — advisory only. PURE and deterministic (sorted by
// docPath, then findingId).
func FindingsToSuggestCandidates(findings []DriftFinding, docModes map[string]workspace.WorkspaceMode) SuggestCandidateMapping {
	candidates := []DriftSuggestCandidate{}
	advisoryOnly := []DriftAdvisoryOnly{}
	for _, finding := range findings {
		rawMode, mapped := docModes[finding.DocPath]
		var mode *workspace.WorkspaceMode
		if mapped && workspace.IsWorkspaceMode(string(rawMode)) {
			resolved := rawMode
			mode = &resolved
		}
		if mode == nil || isProtectedMode(*mode) {
			// Ownership floor (D2): locked/generated/external resolve blocked
			// at ANY confidence — never a patch candidate; the finding stays
			// advisory.
			reason := "doc mode unknown (not in the caller-supplied mode map) — fail closed to advisory"
			if mode != nil {
				reason = fmt.Sprintf(
					"ownership-floor mode %q would resolve blocked at any confidence (D2) — advisory only",
					string(*mode))
			}
			advisoryOnly = append(advisoryOnly, DriftAdvisoryOnly{
				DocPath:   finding.DocPath,
				FindingID: finding.ID,
				Mode:      mode,
				Reason:    reason,
			})
			continue
		}
		candidates = append(candidates, DriftSuggestCandidate{
			DocPath:   finding.DocPath,
			Mode:      *mode,
			ClaimID:   finding.ClaimID,
			FindingID: finding.ID,
			Kind:      finding.Kind,
			Reason:    finding.Evidence.Detail,
			Commits:   copyStrings(finding.Evidence.Commits),
			Paths:     copyStrings(finding.Evidence.Paths),
			Trigger:   DriftSuggestTriggerLoopB,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].DocPath != candidates[j].DocPath {
			return candidates[i].DocPath < candidates[j].DocPath
		}
		return candidates[i].FindingID < candidates[j].FindingID
	})
	sort.SliceStable(advisoryOnly, func(i, j int) bool {
		if advisoryOnly[i].DocPath != advisoryOnly[j].DocPath {
			return advisoryOnly[i].DocPath < advisoryOnly[j].DocPath
		}
		return advisoryOnly[i].FindingID < advisoryOnly[j].FindingID
	})
	return SuggestCandidateMapping{Candidates: candidates, AdvisoryOnly: advisoryOnly}
}

// isProtectedMode reports whether mode is in the D2 ownership floor
// (TS `(PROTECTED_MODES as readonly string[]).includes(mode)`).
func isProtectedMode(mode workspace.WorkspaceMode) bool {
	for _, protected := range workspace.ProtectedModes {
		if string(mode) == protected {
			return true
		}
	}
	return false
}

/* --------------------------------------------------------------- the flow */

// DriftSuggestFlowDeps are the dependencies of RunDriftSuggestFlow (W5b2
// wiring surface).
type DriftSuggestFlowDeps struct {
	// Candidates are the evidence-only candidates (from
	// FindingsToSuggestCandidates).
	Candidates []DriftSuggestCandidate
	// Input is the Loop-A pipeline input, composed by the caller EXACTLY as
	// the PR path composes it (manifest, policy payload, analyzer lineup,
	// clock). The change window (base/head) is the caller's derivation from
	// the finding evidence.
	Input *PipelineInput
	// Ports is the Loop-A ports bundle, composed by the caller EXACTLY as the
	// PR path composes it (decision/writer/verification/policy). The SHARED
	// policy stage inside decides every outcome — the drift path re-derives
	// nothing.
	Ports PipelinePorts
	// PipelineEntry is the shared pipeline entry (the identity seam for
	// V-P11-10). A NIL entry invokes nothing — fail closed until the wiring
	// wave binds the shared orchestrator entry (see seams.go).
	PipelineEntry LoopAPipelineEntry
}

// DriftSuggestFlowResult is the flow outcome.
type DriftSuggestFlowResult struct {
	// Trigger is the trigger recorded for this flow — always `loop-b`.
	Trigger DriftSuggestTrigger
	// Candidates are the candidates the flow was fed (evidence only;
	// trigger-tagged).
	Candidates []DriftSuggestCandidate
	// Outcome is the pipeline outcome (nil when nothing was fed into the
	// entry).
	Outcome *schemas.RunOutcome
	// PipelineEntry is the pipeline entry actually used (identity surface for
	// V-P11-10).
	PipelineEntry LoopAPipelineEntry
}

// RunDriftSuggestFlow feeds the drift SUGGEST candidates into the SAME Loop-A
// pipeline entry the PR trigger uses (one code path, two triggers — ARCH
// §7.2). Candidates outside the manifest allowlist (D7) never enter the run; a
// flow with NO in-scope candidates invokes nothing. The pipeline's own
// verification + policy stages produce the outcome vocabulary, which is passed
// through VERBATIM (no outcome re-derivation in the drift path).
func RunDriftSuggestFlow(ctx context.Context, deps DriftSuggestFlowDeps) (DriftSuggestFlowResult, error) {
	entry := deps.PipelineEntry
	if len(deps.Candidates) == 0 {
		return DriftSuggestFlowResult{
			Trigger:       DriftSuggestTriggerLoopB,
			Candidates:    []DriftSuggestCandidate{},
			Outcome:       nil,
			PipelineEntry: entry,
		}, nil
	}
	// D7 allowlist gate: a candidate outside the manifest never enters the
	// run.
	if deps.Input == nil || deps.Input.Manifest == nil {
		// No manifest allowlist: nothing is in scope (fail closed, D7) —
		// the entry is never invoked with an unallowlisted candidate.
		return DriftSuggestFlowResult{
			Trigger:       DriftSuggestTriggerLoopB,
			Candidates:    deps.Candidates,
			Outcome:       nil,
			PipelineEntry: entry,
		}, nil
	}
	manifestPaths := make(map[string]struct{}, len(deps.Input.Manifest.Documents))
	for _, doc := range deps.Input.Manifest.Documents {
		manifestPaths[doc.Path] = struct{}{}
	}
	inScope := 0
	for _, candidate := range deps.Candidates {
		if _, ok := manifestPaths[candidate.DocPath]; ok {
			inScope++
		}
	}
	if inScope == 0 || entry == nil {
		return DriftSuggestFlowResult{
			Trigger:       DriftSuggestTriggerLoopB,
			Candidates:    deps.Candidates,
			Outcome:       nil,
			PipelineEntry: entry,
		}, nil
	}
	outcome, err := entry(ctx, deps.Input, deps.Ports)
	if err != nil {
		return DriftSuggestFlowResult{}, err
	}
	return DriftSuggestFlowResult{
		Trigger:       DriftSuggestTriggerLoopB,
		Candidates:    deps.Candidates,
		Outcome:       outcome,
		PipelineEntry: entry,
	}, nil
}
