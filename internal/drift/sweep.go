package drift

// Ported from core/src/drift/sweep.ts — the claim-support sweep, the headline
// query (P11-T17; ARCH §4.3; CTX §11; PLAN §12.3 F11-9, V-P11-7). It iterates
// the claim store and answers, per claim, whether its support still holds in
// the source of truth at HEAD:
//
//     Which documented claims lost support in the source of truth?
//
// Support determination reuses the EXISTING deterministic predicates — no
// second, divergent support predicate is written:
//
//   - Anchors: claims.ResolveAnchor — content-hash-first, line-number-second,
//     the exact Phase-1 anchoring identity (invariant 13: a nowhere/ambiguous
//     hash match is `unanchored`, never a silent re-anchor). The anchor doc is
//     read at HEAD (the source of truth), not from the working tree.
//   - Source support: ARCH §4.3 claim-record semantics — a claim's source is
//     "code file:line + blob SHA"; the backing blob identity at head decides:
//     intact ⇒ `supported`; moved or the file gone ⇒ `lost-support` (the
//     recorded backing bytes no longer exist — ARCH §4.3's direct-invalidation
//     posture, the same direction the W2 rule table and tier-2 head
//     verification refute in; the sweep's status targets are dispatched
//     through the SAME claim state machine functions the W2 wiring uses).
//
// STATE DISCIPLINE: the sweep is a QUERY. It writes NOTHING by default
// (`Apply` flag, default false → report-only). When the caller explicitly
// opts in, a newly-lost-support claim flips ONLY through the claim state
// machine's own CAS transition functions (claims.LoseSupport for refuted
// support, claims.MarkUnanchored for failed anchor resolution) with the
// caller's RunID — never a direct store write, never silent. Only `verified`
// claims flip (newly lost support); every applied flip and every CAS conflict
// (R15 — the loser re-runs, never overwrites) is recorded in the report. Git
// failures are typed *gitx.GitExecErrors and propagate (D14) — only the
// DECLARED absences (missing doc/source blob at head) become verdicts.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/DUDE616/docSync-go/internal/claims"
	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitx"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/DUDE616/docSync-go/internal/state"
)

// DefaultCommitWalkDepth is the default bounded walk depth for per-path commit
// evidence (TS DEFAULT_COMMIT_WALK_DEPTH).
const DefaultCommitWalkDepth = 50

// MaxCommitWalkDepth is the hard cap for the commit-evidence walk depth
// (TS MAX_COMMIT_WALK_DEPTH).
const MaxCommitWalkDepth = 500

// ClampCommitWalkDepth clamps the caller's commit-evidence walk depth into
// [1, MaxCommitWalkDepth] (deterministic); nil keeps DefaultCommitWalkDepth.
func ClampCommitWalkDepth(depth *int) int {
	requested := DefaultCommitWalkDepth
	if depth != nil {
		requested = *depth
	}
	if requested < 1 {
		return 1
	}
	if requested > MaxCommitWalkDepth {
		return MaxCommitWalkDepth
	}
	return requested
}

// ClaimSupportSweepInput is the sweep input bag.
type ClaimSupportSweepInput struct {
	// Claims are the claims to sweep (typically ClaimStore.List() — sorted,
	// deterministic).
	Claims []schemas.Claim
	// Git is the typed git client (the only git surface used here).
	Git *gitx.GitClient
	// Head is the head rev (ref or sha) — the source of truth the sweep
	// evaluates against.
	Head string
	// RunID is the owning run id — recorded on any appended history event.
	RunID string
	// At is the ISO-8601 stamp (caller-owned; no hidden clock).
	At string
	// Apply is the explicit state-flip opt-in. Default (false): REPORT-ONLY —
	// zero writes of any kind. True requires Store.
	Apply bool
	// Store is the ClaimStore to transition through when Apply is true.
	Store *claims.ClaimStore
	// ExpectedHashByClaim carries per-claim CAS expectations for flips (a
	// stale hash surfaces a conflict, per the machine).
	ExpectedHashByClaim map[string]string
	// CommitWalkDepth is the bounded walk depth for per-path commit evidence
	// (default 50, hard cap 500); nil means "unset" (the default).
	CommitWalkDepth *int
}

// blobShaOrNull returns the blob sha of `<rev>:<path>`, or nil when the object
// does not exist (a DECLARED absence → a verdict, not an error). Any other git
// failure propagates as a typed *gitx.GitExecError (D14 — never swallowed into
// a verdict).
func blobShaOrNull(ctx context.Context, git *gitx.GitClient, rev, path string) (*string, error) {
	cat, err := git.CatFile(ctx, rev, path)
	if err != nil {
		if gitErr, ok := gitx.AsGitExecError(err); ok && gitErr.Kind == gitx.KindMissingRev {
			return nil, nil
		}
		return nil, err
	}
	sha := cat.BlobSha
	return &sha, nil
}

// touchingCommits returns the commits from head's bounded `--topo-order` walk
// that touch `path` (patch-id per commit scoped to the path — the same
// deterministic walk primitive the D12 ladder uses). Walk order is kept
// (deterministic by graph shape, no date dependence); errors propagate (D14).
func touchingCommits(ctx context.Context, git *gitx.GitClient, headSha, path string, depth int) ([]string, error) {
	commits, err := git.LogCommits(ctx, headSha, depth)
	if err != nil {
		return nil, err
	}
	touching := make([]string, 0, len(commits))
	for _, commit := range commits {
		if len(commit.Parents) == 0 {
			continue // root commit: no own patch
		}
		parent := commit.Parents[0]
		patchID, err := git.PatchId(ctx, parent, commit.Sha, &path)
		if err != nil {
			return nil, err
		}
		if patchID != "" {
			touching = append(touching, commit.Sha)
		}
	}
	return touching, nil
}

// shortHead is the TS `headSha.slice(0, 12)` used in the recorded flip reason.
func shortHead(headSha string) string {
	if len(headSha) > 12 {
		return headSha[:12]
	}
	return headSha
}

// RunClaimSupportSweep runs the P11-T17 claim-support sweep (see module doc
// for the predicate reuse and the state discipline). Deterministic and
// zero-LLM; the returned report is validated against
// `docsync.claim-support-report/v1` before it is returned (fail closed on any
// producer bug).
func RunClaimSupportSweep(ctx context.Context, input ClaimSupportSweepInput) (*schemas.ClaimSupportReport, error) {
	if input.Git == nil {
		return nil, docutil.New(docutil.CodeInternal,
			"runClaimSupportSweep: git client is nil — refusing to sweep without git (fail closed)")
	}
	if input.Apply && input.Store == nil {
		return nil, state.StateKeyError(
			"claim-support sweep: apply: true requires a claim store — state flips ONLY through the claim state machine, never a direct write")
	}
	headSha, err := input.Git.ResolveRev(ctx, input.Head)
	if err != nil {
		return nil, err
	}
	depth := ClampCommitWalkDepth(input.CommitWalkDepth)

	sortedClaims := append([]schemas.Claim(nil), input.Claims...)
	sort.SliceStable(sortedClaims, func(i, j int) bool {
		return compareStrings(sortedClaims[i].ID, sortedClaims[j].ID) < 0
	})

	entries := make([]schemas.ClaimSupportEntry, 0, len(sortedClaims))
	flips := make([]schemas.ClaimSupportFlip, 0)
	conflicts := make([]schemas.ClaimSupportConflict, 0)
	commitsByPath := map[string][]string{} // per-path commit evidence, computed once

	for _, claim := range sortedClaims {
		var verdict schemas.ClaimSupportVerdict
		var reason string
		var anchorResolution schemas.ClaimSupportAnchorResolution
		var blobAtHead *string

		// 1. Anchor resolution — the existing content-hash-first predicate.
		docContent, err := docContentAtHead(ctx, input.Git, headSha, claim.Anchor.File)
		if err != nil {
			return nil, err
		}
		if docContent == nil {
			verdict = schemas.ClaimSupportUnresolvableAnchor
			anchorResolution = schemas.ClaimSupportAnchorUnanchored
			reason = fmt.Sprintf(`anchor doc "%s" is not readable at head — anchor cannot be resolved`,
				claim.Anchor.File)
			blobAtHead, err = blobShaOrNull(ctx, input.Git, headSha, claim.Source.File)
			if err != nil {
				return nil, err
			}
		} else {
			resolution := claims.ResolveAnchor(claim.Anchor, *docContent)
			if resolution.Status == claims.AnchorUnanchored {
				verdict = schemas.ClaimSupportUnresolvableAnchor
				anchorResolution = schemas.ClaimSupportAnchorUnanchored
				reason = resolution.Reason
				blobAtHead, err = blobShaOrNull(ctx, input.Git, headSha, claim.Source.File)
				if err != nil {
					return nil, err
				}
			} else {
				anchorResolution = schemas.ClaimSupportAnchorResolution(resolution.Status)
				// 2. Source support — the ARCH §4.3 blob-identity predicate.
				blobAtHead, err = blobShaOrNull(ctx, input.Git, headSha, claim.Source.File)
				if err != nil {
					return nil, err
				}
				switch {
				case blobAtHead == nil:
					verdict = schemas.ClaimSupportLostSupport
					reason = fmt.Sprintf(`source file "%s" is missing at head — the recorded backing source is gone`,
						claim.Source.File)
				case *blobAtHead == claim.Source.BlobSHA:
					verdict = schemas.ClaimSupportSupported
					reason = "source blob identity intact at head"
				default:
					verdict = schemas.ClaimSupportLostSupport
					reason = fmt.Sprintf(
						"source blob moved at head (recorded %s, head %s) — the recorded backing bytes no longer exist",
						claim.Source.BlobSHA, *blobAtHead)
				}
			}
		}

		// Commit evidence (computed only for lost-support claims, once per path).
		commits := []string{}
		if verdict == schemas.ClaimSupportLostSupport {
			cached, ok := commitsByPath[claim.Source.File]
			if !ok {
				cached, err = touchingCommits(ctx, input.Git, headSha, claim.Source.File, depth)
				if err != nil {
					return nil, err
				}
				commitsByPath[claim.Source.File] = cached
			}
			commits = cached
		}

		evidence := schemas.ClaimSupportEvidence{
			ClaimID:          claim.ID,
			AnchorFile:       claim.Anchor.File,
			AnchorStartLine:  claim.Anchor.StartLine,
			AnchorEndLine:    claim.Anchor.EndLine,
			AnchorResolution: anchorResolution,
			SourceFile:       claim.Source.File,
			SourceBlobSHA:    claim.Source.BlobSHA,
			Commits:          commits,
			Detail:           claimSupportDetail(verdict),
		}
		if claim.Source.Line != nil {
			line := *claim.Source.Line
			evidence.SourceLine = &line
		}
		if blobAtHead != nil {
			sha := *blobAtHead
			evidence.SourceBlobSHAAtHead = &sha
		}
		entries = append(entries, schemas.ClaimSupportEntry{
			ClaimID:      claim.ID,
			Verdict:      verdict,
			StatusBefore: claim.Status,
			Reason:       reason,
			Evidence:     evidence,
		})

		// 3. State discipline: flips ONLY on explicit opt-in, ONLY for newly
		//    lost support (`verified` claims), ONLY through the machine's own
		//    CAS transitions — every outcome recorded, conflicts surfaced (R15).
		if !input.Apply || input.Store == nil {
			continue
		}
		if claim.Status != schemas.ClaimStatusVerified {
			continue // only NEWLY lost support flips
		}
		var expectedHash *string
		if hash, ok := input.ExpectedHashByClaim[claim.ID]; ok {
			expectedHash = &hash
		}
		// Dispatch is VERBATIM from sweep.ts: refuted support (blob moved /
		// source gone) goes through loseSupport, every other verdict (anchor
		// resolution failed) through markUnanchored. The TS applies this
		// dispatch to every `verified` claim as-is — ported unchanged on
		// purpose (CONVENTIONS.md: no silent behavior changes); the caller is
		// expected to sweep claims at risk, not the whole verified store.
		transition := schemas.ClaimSupportTransitionLoseSupport
		if verdict != schemas.ClaimSupportLostSupport {
			transition = schemas.ClaimSupportTransitionMarkUnanchored
		}
		options := claims.ClaimTransitionOptions{
			RunID:        input.RunID,
			At:           input.At,
			Reason:       fmt.Sprintf("claim-support sweep at head %s: %s", shortHead(headSha), reason),
			ExpectedHash: expectedHash,
		}
		var write claims.ClaimTransitionResult
		if verdict == schemas.ClaimSupportLostSupport {
			write, err = claims.LoseSupport(input.Store, claim.ID, options)
		} else {
			write, err = claims.MarkUnanchored(input.Store, claim.ID, options)
		}
		if err != nil {
			return nil, err
		}
		if write.OK {
			flips = append(flips, schemas.ClaimSupportFlip{
				ClaimID:    claim.ID,
				From:       claim.Status,
				To:         write.Claim.Status,
				Transition: transition,
				Event:      lastHistoryEvent(write.Claim),
				Hash:       write.Hash,
			})
			continue
		}
		conflicts = append(conflicts, schemas.ClaimSupportConflict{
			ClaimID:      claim.ID,
			Transition:   transition,
			ExpectedHash: write.ExpectedHash,
			ActualHash:   write.ActualHash,
		})
	}

	report := schemas.ClaimSupportReport{
		Schema:    schemas.ClaimSupportReportSchemaID,
		RunID:     input.RunID,
		At:        input.At,
		Head:      headSha,
		Applied:   input.Apply,
		Claims:    entries,
		Flips:     flips,
		Conflicts: conflicts,
		Summary: schemas.ClaimSupportSummary{
			Total:              len(entries),
			Supported:          countVerdict(entries, schemas.ClaimSupportSupported),
			LostSupport:        countVerdict(entries, schemas.ClaimSupportLostSupport),
			UnresolvableAnchor: countVerdict(entries, schemas.ClaimSupportUnresolvableAnchor),
			Flipped:            len(flips),
		},
	}
	// Fail closed on any producer bug: the report must satisfy its own schema.
	return validateClaimSupportReport(report)
}

// docContentAtHead reads the anchor doc at `<head>:<path>`: content, or nil
// when the object does not exist (missing-rev — a DECLARED absence, not an
// error; any other git failure propagates, D14).
func docContentAtHead(ctx context.Context, git *gitx.GitClient, headSha, path string) (*string, error) {
	cat, err := git.CatFile(ctx, headSha, path)
	if err != nil {
		if gitErr, ok := gitx.AsGitExecError(err); ok && gitErr.Kind == gitx.KindMissingRev {
			return nil, nil
		}
		return nil, err
	}
	content := cat.Content
	return &content, nil
}

// claimSupportDetail is the verdict's evidence detail line.
func claimSupportDetail(verdict schemas.ClaimSupportVerdict) string {
	switch verdict {
	case schemas.ClaimSupportSupported:
		return "backing blob identity matches the recorded source anchor (ARCH §4.3)"
	case schemas.ClaimSupportLostSupport:
		return "recorded backing bytes no longer exist at head (ARCH §4.3 direct-invalidation posture)"
	default:
		return "anchor resolution failed — routes REVIEW (invariant 13, never a silent re-anchor)"
	}
}

// countVerdict counts the entries with one verdict.
func countVerdict(entries []schemas.ClaimSupportEntry, verdict schemas.ClaimSupportVerdict) int {
	count := 0
	for _, entry := range entries {
		if entry.Verdict == verdict {
			count++
		}
	}
	return count
}

// lastHistoryEvent is the machine's just-appended history event (the TS
// `history.at(-1)?.event ?? ”`).
func lastHistoryEvent(claim *schemas.Claim) string {
	if claim == nil || len(claim.History) == 0 {
		return ""
	}
	return claim.History[len(claim.History)-1].Event
}

// validateClaimSupportReport is the Go rendering of the TS
// `CLAIM_SUPPORT_REPORT_SCHEMA.parse` on the produced report: the value is
// serialized and strictly parsed back against
// `docsync.claim-support-report/v1`, and the PARSED report is returned.
func validateClaimSupportReport(report schemas.ClaimSupportReport) (*schemas.ClaimSupportReport, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return schemas.ParseClaimSupportReport(raw)
}
