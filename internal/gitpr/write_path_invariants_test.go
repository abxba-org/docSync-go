package gitpr_test

// Black-box port of test/write-path-invariants-p12-t17.property.test.ts
// (P12-T17; PLAN §7.3 "PR-side safety"; ARCH §12.2, §11.4 layer 3; invariants
// 2/3/4/9).
//
// The TS suite fuzzed the FULL write path with fast-check — the Phase 5
// orchestrator, nine permission gates and the composed doc-PR flow. Those
// surfaces belong to other delegates (doc-pr-flow.go, the governors, the
// idempotency pipeline), so this file ports only the invariants that target the
// files THIS unit owns, with the fast-check generators rewritten as a seeded
// deterministic case generator (same spirit: fixed seed ⇒ fixed cases ⇒ no
// flakes, no network, no wall clock):
//
//   - I.12 candidate-doc gated: a document rides the PR only when its gate
//     passed; everything else is REPORTED as dropped, never silently.
//   - write-path safety: nothing that reaches a write path is an
//     authority-config surface, and every refusal that targets one is recorded
//     as model_influence_on_authority evidence (fail loud, append-only).
//   - partial-pass determinism: input order is preserved for both lists, and the
//     rendered report is byte-identical across runs.
//   - terminal failure routes to REVIEW: a verify failure drops the doc from the
//     PR; a calibration feedback record is either schema-valid or refused
//     (never silently emitted).
//   - state identity: recording the same incident through a different sink/dir
//     reproduces the same deterministic `inc-<sha16>` id, and the log file grows
//     by exactly one line per record (append-only, never rewritten).
//   - supersede decision: the matrix never guesses — update iff the idempotency
//     key matches, supersede iff an open PR exists with a different key, create
//     iff none exists.
//   - post-merge: shouldRerun implies every named docsync PR is based on the
//     default branch; a non-default-branch push never re-runs.
//
// 250 deterministic seeded cases per property (the TS PR's 200-run budget), with
// the case count asserted so a shrinking generator cannot silently drop
// coverage.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/require"
)

// writePathPropertyCases is the port's case budget (the TS PR ran 200 fast-check
// runs; the Go port runs one more so the count is never accidentally at the
// boundary).
const writePathPropertyCases = 250

// writePathIncidentID / writePathFeedbackID pin the id shapes.
var (
	writePathIncidentID = regexp.MustCompile(`^inc-[0-9a-f]{16}$`)
	writePathFeedbackID = regexp.MustCompile(`^fb-[0-9a-f]{16}$`)
)

/* ------------------------------------------------- the seeded case generator */

// writePathLCG is a small deterministic generator (xorshift64*) — the Go
// stand-in for fast-check's seeded arbitraries.
type writePathLCG struct{ state uint64 }

func newWritePathLCG(seed uint64) *writePathLCG {
	if seed == 0 { // a zero state is degenerate for xorshift64*
		seed = 0x9E3779B97F4A7C15
	}
	return &writePathLCG{state: seed}
}

func (l *writePathLCG) next() uint64 {
	l.state ^= l.state >> 12
	l.state ^= l.state << 25
	l.state ^= l.state >> 27
	return l.state * 0x2545F4914F6CDD1D
}

func (l *writePathLCG) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(l.next() % uint64(n))
}

func (l *writePathLCG) bool() bool { return l.next()&1 == 0 }

// writePathDocNames is the doc-path universe the generator draws from.
var writePathDocNames = []string{
	"docs/payments.md", "docs/api.md", "docs/cli.md", "docs/arch.md",
	"docs/legacy.md", "docs/index.md", "docs/audit.md",
}

func (l *writePathLCG) docPath() string { return writePathDocNames[l.intn(len(writePathDocNames))] }

func (l *writePathLCG) verdict() schemas.VerificationVerdict {
	switch l.intn(3) {
	case 0:
		return schemas.VerdictPass
	case 1:
		return schemas.VerdictReview
	default:
		return schemas.VerdictReject
	}
}

func (l *writePathLCG) detail() string {
	switch l.intn(4) {
	case 0:
		return ""
	case 1:
		return "anchors | unresolved"
	case 2:
		return "anchors unresolved\nacross lines"
	default:
		return fmt.Sprintf("verification reported %s", l.verdict())
	}
}

func (l *writePathLCG) authorityishPath() string {
	switch l.intn(6) {
	case 0:
		return gitpr.AuthorityConfigDoc
	case 1:
		return gitpr.AuthorityConfigModels
	case 2:
		return gitpr.AuthorityConfigOrgPolicy
	case 3:
		return fmt.Sprintf("%spromotion-%d.yml", gitpr.AuthorityConfigPoliciesPrefix, l.intn(5))
	case 4:
		return "src/handlers/payments.ts"
	default:
		return "docs/payments.md"
	}
}

func (l *writePathLCG) candidate() gitpr.PartialPassCandidate {
	report := &schemas.VerificationReport{
		Schema:        "docsync.verification-report/v1",
		RunID:         "run-prop",
		DocPath:       l.docPath(),
		PatchRef:      "patch-ref",
		Verdict:       l.verdict(),
		AutoEligible:  l.bool(),
		PolicyVersion: 17,
	}
	return gitpr.PartialPassCandidate{
		DocPath:      l.docPath(),
		Verified:     l.bool(),
		Verification: report,
		Detail:       l.detail(),
	}
}

func (l *writePathLCG) reviewerEvent() gitpr.ReviewerEvent {
	switch l.intn(7) {
	case 0:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventMerged}
	case 1:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges, EditedDiff: "- stale\n+ fresh\n"}
	case 2:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewChanges}
	case 3:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventClosedRejected, StatedReason: l.rejectReason()}
	case 4:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventLabel, Label: l.label()}
	case 5:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventTimeout}
	default:
		return gitpr.ReviewerEvent{Kind: gitpr.ReviewerEventReviewComment}
	}
}

func (l *writePathLCG) rejectReason() string {
	switch l.intn(3) {
	case 0:
		return "wrong tone"
	case 1:
		return "this is already  correct"
	default:
		return ""
	}
}

func (l *writePathLCG) label() string {
	switch l.intn(3) {
	case 0:
		return "docsync:intentional"
	case 1:
		return "priority"
	default:
		return ""
	}
}

func (l *writePathLCG) pushRef() string {
	switch l.intn(3) {
	case 0:
		return "main"
	case 1:
		return "refs/heads/main"
	default:
		return fmt.Sprintf("refs/heads/feature-%d", l.intn(4))
	}
}

func (l *writePathLCG) openPrs() []gitpr.OpenDocsyncPr {
	count := l.intn(4)
	prs := make([]gitpr.OpenDocsyncPr, 0, count)
	for i := 0; i < count; i++ {
		base := "main"
		if l.bool() {
			base = "release"
		}
		prs = append(prs, gitpr.OpenDocsyncPr{Number: i + 1, Base: base})
	}
	return prs
}

func (l *writePathLCG) candidates() []gitpr.PartialPassCandidate {
	count := l.intn(6)
	candidates := make([]gitpr.PartialPassCandidate, 0, count)
	for i := 0; i < count; i++ {
		candidates = append(candidates, l.candidate())
	}
	return candidates
}

/* ------------------------------------------- I.12 candidate-doc gated (P6-T10) */

func TestWritePathInvariant_I12CandidateDocGated(t *testing.T) {
	total := 0
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		candidates := lcg.candidates()
		total += len(candidates)

		result, err := gitpr.AssemblePartialPass(candidates)
		require.NoError(t, err, "seed %d", seed)

		// The candidate list is PARTITIONED: included ∪ dropped === input, and a
		// doc rides the PR only when its verification gate passed.
		require.Len(t, result.Included, countMatching(candidates, true), "seed %d: included = gate-passed", seed)
		require.Len(t, result.Dropped, countMatching(candidates, false), "seed %d: dropped = gate-failed", seed)
		for _, included := range result.Included {
			require.NotEmpty(t, included.DocPath, "seed %d", seed)
		}
		for _, dropped := range result.Dropped {
			require.NotEmpty(t, dropped.DocPath, "seed %d", seed)
			require.Equal(t, gitpr.PartialPassDropReason, dropped.Reason, "seed %d", seed)
			require.NotEmpty(t, dropped.Detail, "seed %d: a dropped doc is a REPORTED doc (D4)", seed)
		}
	}
	require.GreaterOrEqual(t, total, 2*writePathPropertyCases,
		"the generator must produce real candidate lists, not degenerate empties")
}

func countMatching(candidates []gitpr.PartialPassCandidate, verified bool) int {
	count := 0
	for _, candidate := range candidates {
		if candidate.Verified == verified {
			count++
		}
	}
	return count
}

func TestWritePathInvariant_PartialPassOrderAndReportAreDeterministic(t *testing.T) {
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		candidates := lcg.candidates()

		first, err := gitpr.AssemblePartialPass(candidates)
		require.NoError(t, err, "seed %d", seed)
		second, err := gitpr.AssemblePartialPass(candidates)
		require.NoError(t, err, "seed %d", seed)

		// Input order is preserved for both lists (ARCH §13 determinism).
		require.Equal(t, first.Included, second.Included, "seed %d", seed)
		require.Equal(t, first.Dropped, second.Dropped, "seed %d", seed)
		includedIndex, droppedIndex := 0, 0
		for _, candidate := range candidates {
			if candidate.Verified {
				if includedIndex < len(first.Included) {
					require.Equal(t, candidate.DocPath, first.Included[includedIndex].DocPath, "seed %d", seed)
				}
				includedIndex++
				continue
			}
			if droppedIndex < len(first.Dropped) {
				require.Equal(t, candidate.DocPath, first.Dropped[droppedIndex].DocPath, "seed %d", seed)
			}
			droppedIndex++
		}

		rendered := gitpr.RenderPartialPassReport(first)
		require.Equal(t, rendered, gitpr.RenderPartialPassReport(second), "seed %d: byte-identical report", seed)
		require.Contains(t, rendered, "## Partial pass (D4)")
		require.Contains(t, rendered, "Included documents (verification passed — merge-worthy):")
		require.Contains(t, rendered, "Dropped documents (verification not passed — reported, never silently):")
		require.True(t, strings.HasSuffix(rendered, "\n"), "seed %d", seed)
		require.False(t, strings.HasSuffix(rendered, "\n\n"), "seed %d", seed)
		for _, doc := range first.Dropped {
			require.Contains(t, rendered, doc.DocPath, "seed %d: the drop surfaces in the PR body", seed)
			require.Contains(t, rendered, doc.Reason, "seed %d", seed)
		}
	}
}

/* -------------------------------- write-path safety + authority influence (OD-R50) */

func TestWritePathInvariant_AuthoritySurfacesAreRefusedAndRecorded(t *testing.T) {
	closedSet := map[string]bool{
		gitpr.AuthorityConfigDoc:       true,
		gitpr.AuthorityConfigModels:    true,
		gitpr.AuthorityConfigOrgPolicy: true,
	}
	expected := func(path string) bool {
		return closedSet[path] || strings.HasPrefix(path, gitpr.AuthorityConfigPoliciesPrefix)
	}

	cases := 0
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		for i := 0; i < 4; i++ {
			path := lcg.authorityishPath()
			cases++
			isSurface := gitpr.IsAuthorityConfigSurface(path)
			require.Equal(t, expected(path), isSurface, "seed %d / path %q", seed, path)

			stateDir := filepath.Join(t.TempDir(), "state")
			sink, err := gitpr.FileIncidentSink(stateDir)
			require.NoError(t, err)
			log := gitpr.CreateIncidentLog(sink)

			// A refusal that targets an authority surface is recorded as
			// model_influence_on_authority evidence; an ordinary
			// out-of-allowlist path is recorded as out-of-allowlist. Either way
			// the incident IS written (fail loud) with the path echoed verbatim.
			if isSurface {
				emitted, err := gitpr.EmitAuthorityInfluenceIncident(context.Background(), log, gitpr.IncidentEmitInput{
					At: "2026-10-07T00:00:00.000Z", RunID: "run-prop", OffendingPath: path, Detail: "refused",
				})
				require.NoError(t, err, "seed %d", seed)
				require.Equal(t, schemas.SafetyIncidentRuleModelInfluenceOnAuthority, emitted.Rule, "seed %d", seed)
				require.Equal(t, path, emitted.OffendingPath, "seed %d", seed)
				require.Regexp(t, writePathIncidentID, emitted.IncidentID, "seed %d", seed)
			} else {
				emitted, err := log.Record(context.Background(), gitpr.RecordIncidentInput{
					At: "2026-10-07T00:00:00.000Z", RunID: "run-prop",
					Rule: schemas.SafetyIncidentRuleOutOfAllowlistPath, OffendingPath: path, Detail: "refused",
				})
				require.NoError(t, err, "seed %d", seed)
				require.Equal(t, path, emitted.OffendingPath, "seed %d", seed)
			}
			require.Len(t, writePathIncidentLines(t, stateDir), 1, "seed %d: exactly one appended line", seed)
		}
	}
	require.GreaterOrEqual(t, cases, writePathPropertyCases, "path cases must be >= 200")
}

/* ----------------------------------------- state identity (append-only log) */

func TestWritePathInvariant_IncidentLogIdentityIsContentAddressed(t *testing.T) {
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		path := lcg.authorityishPath()
		rule := schemas.SafetyIncidentRuleOutOfAllowlistPath
		if lcg.bool() {
			rule = schemas.SafetyIncidentRuleProtectedPathPatch
		}
		input := gitpr.RecordIncidentInput{
			At:            "2026-10-07T00:00:00.000Z",
			RunID:         "run-prop",
			Rule:          rule,
			OffendingPath: path,
			Detail:        "refused",
		}

		firstDir := filepath.Join(t.TempDir(), "state-a")
		secondDir := filepath.Join(t.TempDir(), "state-b")
		firstSink, err := gitpr.FileIncidentSink(firstDir)
		require.NoError(t, err)
		secondSink, err := gitpr.FileIncidentSink(secondDir)
		require.NoError(t, err)

		first, err := gitpr.RecordIncident(context.Background(), gitpr.CreateIncidentLog(firstSink), input)
		require.NoError(t, err, "seed %d", seed)
		second, err := gitpr.RecordIncident(context.Background(), gitpr.CreateIncidentLog(secondSink), input)
		require.NoError(t, err, "seed %d", seed)

		// Same inputs → same deterministic id, independent of the sink/dir.
		require.Equal(t, first.IncidentID, second.IncidentID, "seed %d", seed)
		require.Regexp(t, writePathIncidentID, first.IncidentID, "seed %d", seed)

		// Append-only: the log grows by exactly one line per record and the
		// recorded bytes are never rewritten. A different body content-hashes to
		// a different deterministic id.
		before, err := os.ReadFile(filepath.Join(firstDir, gitpr.IncidentLogFilename))
		require.NoError(t, err)
		changed := input
		changed.OffendingPath = path + "-2"
		other, err := gitpr.RecordIncident(context.Background(), gitpr.CreateIncidentLog(firstSink), changed)
		require.NoError(t, err, "seed %d", seed)
		require.NotEqual(t, first.IncidentID, other.IncidentID, "seed %d", seed)
		after, err := os.ReadFile(filepath.Join(firstDir, gitpr.IncidentLogFilename))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(after), string(before)),
			"seed %d: an append only ever extends the log", seed)
		require.Len(t, writePathIncidentLines(t, firstDir), 2, "seed %d", seed)
	}
}

/* ------------------------------------------------------ supersede decision */

func TestWritePathInvariant_SupersedeDecisionNeverGuesses(t *testing.T) {
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		key := fmt.Sprintf("sha%d", lcg.intn(5))
		var existing *gitpr.SupersedenceExistingPr
		if lcg.bool() {
			existingKey := key
			if lcg.bool() {
				existingKey = fmt.Sprintf("sha%d", lcg.intn(5))
			}
			existing = &gitpr.SupersedenceExistingPr{
				PrNumber:       lcg.intn(4) + 1,
				IdempotencyKey: existingKey,
			}
		}

		decision, err := gitpr.EvaluateSupersedence(existing, key)
		require.NoError(t, err, "seed %d", seed)
		require.Contains(t, []string{
			gitpr.SupersedenceActionCreate,
			gitpr.SupersedenceActionUpdate,
			gitpr.SupersedenceActionSupersedeThenCrea,
		}, decision.Action, "seed %d: the action is in the closed union", seed)

		switch {
		case existing == nil:
			require.Equal(t, gitpr.SupersedenceActionCreate, decision.Action, "seed %d", seed)
			require.Zero(t, decision.ExistingPrNumber, "seed %d", seed)
		case existing.IdempotencyKey == key:
			require.Equal(t, gitpr.SupersedenceActionUpdate, decision.Action, "seed %d", seed)
			require.Equal(t, existing.PrNumber, decision.ExistingPrNumber, "seed %d", seed)
		default:
			require.Equal(t, gitpr.SupersedenceActionSupersedeThenCrea, decision.Action, "seed %d", seed)
			require.Equal(t, existing.PrNumber, decision.ExistingPrNumber, "seed %d", seed)
		}

		// Marker shape is preserved for every valid newer-PR number.
		marker, err := gitpr.SupersedeMarker(lcg.intn(9) + 1)
		require.NoError(t, err, "seed %d", seed)
		require.True(t, strings.HasPrefix(marker, gitpr.SupersedeMarkerPrefix), "seed %d", seed)
		require.True(t, strings.HasSuffix(marker, ")"), "seed %d", seed)
	}
}

/* ----------------------------------------------------------- post-merge gate */

func TestWritePathInvariant_PostMergeOnlyReRunsOnDefaultBranch(t *testing.T) {
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		ref := lcg.pushRef()
		prs := lcg.openPrs()

		decision, err := gitpr.EvaluatePostMergeReRun(gitpr.PostMergeReRunInput{
			Event:          gitpr.PostMergePushEvent{Kind: gitpr.PostMergeEventKindPush, Ref: ref},
			DefaultBranch:  "main",
			OpenDocsyncPrs: prs,
		})
		require.NoError(t, err, "seed %d", seed)

		onDefault := ref == "main" || ref == "refs/heads/main"
		affected := 0
		for _, pr := range prs {
			if pr.Base == "main" {
				affected++
			}
		}
		if !onDefault || affected == 0 {
			require.False(t, decision.ShouldRerun, "seed %d: no rerun unless the default branch is touched by default-based PRs", seed)
			continue
		}
		require.True(t, decision.ShouldRerun, "seed %d", seed)
		// Only default-based PRs are named — a differently-based PR is untouched.
		for _, pr := range prs {
			if pr.Base != "main" {
				require.NotContains(t, decision.Reason, fmt.Sprintf("#%d", pr.Number), "seed %d", seed)
			}
		}
		for _, pr := range prs {
			if pr.Base == "main" {
				require.Contains(t, decision.Reason, fmt.Sprintf("#%d", pr.Number), "seed %d", seed)
			}
		}
	}
}

/* --------------------------------------------------- terminal failure routing */

func TestWritePathInvariant_FeedbackRecordsAreValidOrRefused(t *testing.T) {
	for seed := uint64(1); seed <= writePathPropertyCases; seed++ {
		lcg := newWritePathLCG(seed)
		event := lcg.reviewerEvent()
		decisionID := "dec-prop"
		sourcePR := lcg.intn(50) + 1
		clock := func() string { return "2026-10-07T00:00:00.000Z" }

		record, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
			Event:          event,
			RunID:          "run-prop",
			PolicyVersion:  17,
			DecisionID:     &decisionID,
			SourcePrNumber: &sourcePR,
			Clock:          clock,
		})
		if err != nil {
			// Fail closed (never silently emitted): the refusal names the record
			// schema and the offending rule.
			var recordErr *gitpr.FeedbackRecordError
			require.ErrorAs(t, err, &recordErr, "seed %d", seed)
			require.Equal(t, gitpr.EDOCSYNCFeedbackRecord, recordErr.Code(), "seed %d", seed)
			continue
		}
		require.NoError(t, validateFeedbackRecord(record), "seed %d", seed)
		require.Regexp(t, writePathFeedbackID, record.RecordID, "seed %d", seed)
		require.NotEmpty(t, record.Detail, "seed %d", seed)
		require.Contains(t, schemas.FeedbackSignalValues, record.Signal, "seed %d", seed)

		// record_id is a content hash: the same inputs re-emit the same id.
		again, err := gitpr.EmitFeedbackRecord(gitpr.FeedbackCaptureInput{
			Event:          event,
			RunID:          "run-prop",
			PolicyVersion:  17,
			DecisionID:     &decisionID,
			SourcePrNumber: &sourcePR,
			Clock:          clock,
		})
		require.NoError(t, err, "seed %d", seed)
		require.Equal(t, record.RecordID, again.RecordID, "seed %d", seed)
	}
}

/* ------------------------------------------------------------------ helpers */

func writePathIncidentLines(t *testing.T, stateDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, gitpr.IncidentLogFilename))
	if err != nil {
		require.NoError(t, err)
	}
	text := strings.TrimRight(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
