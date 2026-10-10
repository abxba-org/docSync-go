package gitpr_test

// Black-box port of the supersede half of test/gitpr-supersede.test.ts (P6-T12/T13;
// PLAN §7.3 "Rate, burst, idempotency, lifecycle"; ARCH §12.2). The
// safety-incident half of that TS suite lives in incident_log_test.go (P6-T12).
//
// Covers the stale-PR supersede flow this unit owns: the exact marker comment
// and its idempotency (a second invocation never duplicates it), the supersede
// decision matrix (same key ⇒ update, different key with an open PR ⇒
// supersede-then-create, none ⇒ create), and the pure disambiguator — whose
// candidate order is pinned equal to the landed branch factory's scan and whose
// exhaustion bound is the factory's shared MAX_DISAMBIGUATOR_CANDIDATES.
//
// No network, no wall clock (P19) — the clock is a constant. InMemoryPrPlatform
// lives in pr-platform.go (another delegate), so the platform double here is a
// local in-memory stand-in with the same observable behavior the TS suite
// asserted on.

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/require"
)

const supersedeClockAt = "2026-10-07T00:00:00.000Z"

func supersedeClock() string { return supersedeClockAt }

/* ------------------------------------------------------ supersede flow (P6-T13) */

// supersedeFakeClient is the in-memory SupersedePrClient double (the TS suite's
// `supersedeClientOver(new InMemoryPrPlatform(...))`).
type supersedeFakeClient struct {
	mu        sync.Mutex
	comments  map[int][]string
	marked    [][2]int
	appendErr error
}

func newSupersedeFakeClient() *supersedeFakeClient {
	return &supersedeFakeClient{comments: map[int][]string{}}
}

func (c *supersedeFakeClient) ListPrComments(_ context.Context, pr gitpr.SeamPrRef) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.comments[pr.Number]...), nil
}

func (c *supersedeFakeClient) AppendPrComment(_ context.Context, pr gitpr.SeamPrRef, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appendErr != nil {
		return c.appendErr
	}
	c.comments[pr.Number] = append(c.comments[pr.Number], body)
	return nil
}

func (c *supersedeFakeClient) MarkSuperseded(_ context.Context, pr, newerPr gitpr.SeamPrRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marked = append(c.marked, [2]int{pr.Number, newerPr.Number})
	return nil
}

func TestP6T13_SupersedeMarkerIdempotent(t *testing.T) {
	client := newSupersedeFakeClient()
	stale := gitpr.SeamPrRef{Number: 7, Branch: "docsync/pr-7-aaaaaaa", Base: "main"}
	newer := gitpr.SeamPrRef{Number: 9, Branch: "docsync/pr-9-bbbbbbb", Base: "main"}

	marker, err := gitpr.SupersedeMarker(newer.Number)
	require.NoError(t, err)
	require.Equal(t, "superseded by newer analysis (#9)", marker)

	first, err := gitpr.SupersedeStalePr(client, gitpr.SupersedeStalePrInput{
		Stale: stale, Newer: newer, Clock: supersedeClock,
	})
	require.NoError(t, err)
	require.Equal(t, gitpr.SupersedeActionSuperseded, first.Action)
	require.Equal(t, supersedeClockAt, first.At)

	second, err := gitpr.SupersedeStalePr(client, gitpr.SupersedeStalePrInput{
		Stale: stale, Newer: newer, Clock: supersedeClock,
	})
	require.NoError(t, err)
	require.Equal(t, gitpr.SupersedeActionAlready, second.Action)

	// Double invocation → ONE marker comment, ONE lifecycle transition.
	require.Equal(t, []string{marker}, client.comments[stale.Number])
	require.Len(t, client.marked, 1)
	require.Equal(t, [2]int{7, 9}, client.marked[0])
}

func TestP6T13_SupersedeMarkerRejectsNonPositiveNumbers(t *testing.T) {
	for _, number := range []int{0, -1, -42} {
		_, err := gitpr.SupersedeMarker(number)
		require.Error(t, err)
		var supersedeErr *gitpr.SupersedeError
		require.ErrorAs(t, err, &supersedeErr)
		require.Equal(t, gitpr.EDOCSYNCSupersede, supersedeErr.Code())
		require.Contains(t, err.Error(), "newerPrNumber must be a positive integer")
	}
}

func TestP6T13_SupersedeInputIsFailClosed(t *testing.T) {
	client := newSupersedeFakeClient()
	valid := gitpr.SeamPrRef{Number: 7}

	_, err := gitpr.SupersedeStalePr(client, gitpr.SupersedeStalePrInput{Stale: valid, Newer: gitpr.SeamPrRef{Number: 8}})
	require.ErrorContains(t, err, "supersedeStalePr: clock must be a () => string function")

	_, err = gitpr.SupersedeStalePr(client, gitpr.SupersedeStalePrInput{
		Stale: gitpr.SeamPrRef{Number: 0}, Newer: gitpr.SeamPrRef{Number: 8}, Clock: supersedeClock,
	})
	require.ErrorContains(t, err, "supersedeStalePr: stale.number must be a positive integer")

	_, err = gitpr.SupersedeStalePr(client, gitpr.SupersedeStalePrInput{
		Stale: valid, Newer: gitpr.SeamPrRef{Number: 7}, Clock: supersedeClock,
	})
	require.ErrorContains(t, err, "a PR cannot supersede itself (#7)")

	// A platform failure propagates (fail closed) and the lifecycle transition
	// never runs.
	failing := newSupersedeFakeClient()
	failing.appendErr = errSupersedePlatform
	_, err = gitpr.SupersedeStalePr(failing, gitpr.SupersedeStalePrInput{
		Stale: valid, Newer: gitpr.SeamPrRef{Number: 8}, Clock: supersedeClock,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errSupersedePlatform)
	require.Empty(t, failing.marked)
}

var errSupersedePlatform = &supersedeStubError{msg: "github: 500 Internal Server Error"}

type supersedeStubError struct{ msg string }

func (e *supersedeStubError) Error() string { return e.msg }

func TestP6T13_SupersedeDecisionMatrix(t *testing.T) {
	// No existing open doc PR → create.
	decision, err := gitpr.EvaluateSupersedence(nil, "sha:hash")
	require.NoError(t, err)
	require.Equal(t, gitpr.SupersedenceDecision{Action: gitpr.SupersedenceActionCreate}, decision)

	// Same idempotency key → update the existing PR (marker-keyed upsert).
	decision, err = gitpr.EvaluateSupersedence(&gitpr.SupersedenceExistingPr{
		PrNumber: 7, IdempotencyKey: "sha:hash",
	}, "sha:hash")
	require.NoError(t, err)
	require.Equal(t, gitpr.SupersedenceDecision{
		Action: gitpr.SupersedenceActionUpdate, ExistingPrNumber: 7,
	}, decision)

	// Different key with an existing OPEN PR → supersede it, then create.
	decision, err = gitpr.EvaluateSupersedence(&gitpr.SupersedenceExistingPr{
		PrNumber: 7, IdempotencyKey: "sha:oldhash",
	}, "sha:newhash")
	require.NoError(t, err)
	require.Equal(t, gitpr.SupersedenceDecision{
		Action: gitpr.SupersedenceActionSupersedeThenCrea, ExistingPrNumber: 7,
	}, decision)

	// Fail closed on malformed input — never a guessed action.
	_, err = gitpr.EvaluateSupersedence(nil, "")
	require.ErrorContains(t, err, "incomingKey must be a nonempty string")

	_, err = gitpr.EvaluateSupersedence(&gitpr.SupersedenceExistingPr{PrNumber: 0, IdempotencyKey: "k"}, "k")
	require.ErrorContains(t, err, "existing.prNumber must be a positive integer")

	_, err = gitpr.EvaluateSupersedence(&gitpr.SupersedenceExistingPr{PrNumber: 7, IdempotencyKey: ""}, "k")
	require.ErrorContains(t, err, "existing.idempotencyKey must be nonempty")
}

func TestP6T13_DisambiguatorFirstFreeSuffix(t *testing.T) {
	base := "docsync/pr-7-aaaaaaa"
	name, err := gitpr.DisambiguatedBranchName(base, map[string]bool{})
	require.NoError(t, err)
	require.Equal(t, base, name)

	name, err = gitpr.DisambiguatedBranchName(base, map[string]bool{base: true})
	require.NoError(t, err)
	require.Equal(t, base+"-2", name)

	name, err = gitpr.DisambiguatedBranchName(base, map[string]bool{base: true, base + "-2": true})
	require.NoError(t, err)
	require.Equal(t, base+"-3", name)

	// A GAP is skipped over, first free wins.
	name, err = gitpr.DisambiguatedBranchName(base, map[string]bool{base: true, base + "-3": true})
	require.NoError(t, err)
	require.Equal(t, base+"-2", name)

	// Pin equality with the branch factory's deterministic scan order
	// (`<base>`, `<base>-2`, `<base>-3`, … — the createDocBranch candidate
	// formula): for EVERY prefix of that scan, the pure helper picks the very
	// candidate the factory would scan next.
	factoryScan := make([]string, 10)
	for attempt := 0; attempt < 10; attempt++ {
		if attempt == 0 {
			factoryScan[attempt] = base
		} else {
			factoryScan[attempt] = base + "-" + strconv.Itoa(attempt+1)
		}
	}
	for i := range factoryScan {
		taken := map[string]bool{}
		for _, candidate := range factoryScan[:i] {
			taken[candidate] = true
		}
		name, err := gitpr.DisambiguatedBranchName(base, taken)
		require.NoError(t, err)
		require.Equal(t, factoryScan[i], name)
	}

	// Exhaustion at the SHARED bound (the factory's constant) is typed, never silent.
	allTaken := map[string]bool{}
	for attempt := 0; attempt < gitpr.MAX_DISAMBIGUATOR_CANDIDATES; attempt++ {
		if attempt == 0 {
			allTaken[base] = true
		} else {
			allTaken[base+"-"+strconv.Itoa(attempt+1)] = true
		}
	}
	_, err = gitpr.DisambiguatedBranchName(base, allTaken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no free branch name for '"+base+"' after")
	require.Contains(t, err.Error(), "deterministic disambiguator candidates")
	require.Equal(t, gitpr.CodeBranchName, docutil.CodeOf(err))

	_, err = gitpr.DisambiguatedBranchName("", map[string]bool{})
	require.ErrorContains(t, err, "disambiguatedBranchName: base must be a nonempty string")
}
