package gitpr_test

import (
	"errors"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/gitpr-retry.test.ts (withGoneRetry) plus the
// "slot-fits withGoneRetry" composition case of github-rest-platform.test.ts.
// Network-free: the creation call is a fake whose failures are typed fakes;
// the wait primitive is an injected recorder — no real sleeping anywhere.

// grStatusError carries a direct structural `.status` (the TS
// `Object.assign(new Error('Gone'), { status: 410 })` shape).
type grStatusError struct {
	message string
	status  int
}

func (e *grStatusError) Error() string   { return e.message }
func (e *grStatusError) HTTPStatus() int { return e.status }

// grResponseStatusError carries the nested `.response.status` shape.
type grResponseStatusError struct {
	message        string
	responseStatus int
}

func (e *grResponseStatusError) Error() string           { return e.message }
func (e *grResponseStatusError) ResponseHTTPStatus() int { return e.responseStatus }

// grRecorder is the injected delay primitive: it records instead of sleeping.
type grRecorder struct {
	delays []time.Duration
}

func (r *grRecorder) delay(d time.Duration) { r.delays = append(r.delays, d) }

// grCreateCall records one creation attempt of the fake platform call.
type grCreateCall struct {
	branch string
	body   string
}

// grFakeCreate is the creation call site: a scripted function plus a call log,
// standing in for the platform port's createPr.
type grFakeCreate struct {
	calls   []grCreateCall
	results []error // consumed in order; nil entries succeed
}

func (f *grFakeCreate) invoke(branch, body string) (string, error) {
	f.calls = append(f.calls, grCreateCall{branch: branch, body: body})
	index := len(f.calls) - 1
	if index < len(f.results) {
		if err := f.results[index]; err != nil {
			return "", err
		}
	}
	return "pr-" + branch, nil
}

func grGone() error {
	return &grStatusError{message: "Gone", status: 410}
}

func grGoneViaResponse() error {
	return &grResponseStatusError{message: "Gone (response)", responseStatus: 410}
}

func grNotFound() error {
	return &grStatusError{message: "Not Found", status: 404}
}

func TestWithGoneRetry_410OnceThenResolvesReturnsTheResult(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	result, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		if attempts == 1 {
			// exercises the `.response.status` path
			return "", grGoneViaResponse()
		}
		return "pr-created", nil
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.NoError(t, err)
	assert.Equal(t, "pr-created", result)
	assert.Equal(t, 2, attempts)
	// The single wait used the injected delay (no real sleeping).
	assert.Equal(t, []time.Duration{time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_410TwiceThrowsTheLast410AfterExactlyTwoAttempts(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	last := grGone()
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", last
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	// the LAST 410, as-is (never rewrapped)
	require.Error(t, err)
	assert.Same(t, last, err)
	assert.Equal(t, 2, attempts) // default retries: 1 → exactly one extra attempt
	assert.Equal(t, []time.Duration{time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_404PropagatesImmediatelySingleAttempt(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	notFound := grNotFound()
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", notFound
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.Error(t, err)
	assert.Same(t, notFound, err) // non-410 never retried
	assert.Equal(t, 1, attempts)
	assert.Empty(t, recorder.delays) // no wait at all
}

func TestWithGoneRetry_defaultsToOneExtraAttemptAnd1000Ms(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		if attempts == 1 {
			return "", grGone()
		}
		return "ok", nil
	}, &gitpr.GoneRetryOptions{Delay: recorder.delay})

	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, []time.Duration{1000 * time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_customRetryBudgetIsHonored(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", grGone()
	}, &gitpr.GoneRetryOptions{
		Retries: intPtrForTest(2),
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.Error(t, err)
	var statusErr *grStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, 410, statusErr.HTTPStatus())
	assert.Equal(t, 3, attempts) // 1 + 2 extra attempts
	assert.Len(t, recorder.delays, 2)
}

func TestWithGoneRetry_negativeRetryBudgetFailsClosed(t *testing.T) {
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "never-called-with-a-sane-budget", nil
	}, &gitpr.GoneRetryOptions{Retries: intPtrForTest(-1)})

	require.Error(t, err)
	assert.Equal(t, 0, attempts)
}

// TestWithGoneRetry_slotFitsACreationSequence ports the "slot-fits
// withGoneRetry" case: a 410-then-success creation sequence retries the SAME
// request (the marker-keyed upsert keeps it idempotent, INV-11).
func TestWithGoneRetry_slotFitsACreationSequence(t *testing.T) {
	platform := &grFakeCreate{results: []error{grGone(), nil}}

	pr, err := gitpr.WithGoneRetry(func() (string, error) {
		return platform.invoke("docsync/docs-api", "First body.")
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   func(time.Duration) {},
	})

	require.NoError(t, err)
	assert.Equal(t, "pr-docsync/docs-api", pr)
	require.Len(t, platform.calls, 2)
	assert.Equal(t, platform.calls[0].branch, platform.calls[1].branch)
	assert.Equal(t, platform.calls[0].body, platform.calls[1].body)
}

func TestIsGoneMatchingIsStructural(t *testing.T) {
	// Both structural forms retry; anything else does not. Expressed through
	// the public helper so the probe contract stays observable.
	retried := func(err error) bool {
		attempts := 0
		_, got := gitpr.WithGoneRetry(func() (string, error) {
			attempts++
			if attempts == 1 {
				return "", err
			}
			return "second", nil
		}, &gitpr.GoneRetryOptions{DelayMs: intPtrForTest(1), Delay: func(time.Duration) {}})
		return got == nil && attempts == 2
	}
	assert.True(t, retried(grGone()))
	assert.True(t, retried(grGoneViaResponse()))
	assert.False(t, retried(grNotFound()))
	assert.False(t, retried(errors.New("plain failure")))
}

// intPtrForTest is the shared pointer helper for optional option fields.
func intPtrForTest(v int) *int { return &v }
