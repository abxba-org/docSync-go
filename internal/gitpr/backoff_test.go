package gitpr_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/gitpr-governors.test.ts — the
// "backoff (P6-T09)" describe block, plus the deterministic-default jitter
// pins. Network-free: the delay primitive is an injected recorder; the rate
// signal is a typed fake.

// bgRateLimitError carries a structural `.status` in the retry set.
type bgRateLimitError struct {
	message string
	status  int
}

func (e *bgRateLimitError) Error() string   { return e.message }
func (e *bgRateLimitError) HTTPStatus() int { return e.status }

// bgRecorder is the injected delay primitive (never sleeps).
type bgRecorder struct {
	delays []float64
}

func (r *bgRecorder) delay(ms float64) { r.delays = append(r.delays, ms) }

// bgRateLimited builds a rate-limit rejection for the retry set.
func bgRateLimited(status int) error {
	if status == 429 {
		return &bgRateLimitError{message: "Too Many Requests", status: 429}
	}
	return &bgRateLimitError{message: "Forbidden", status: 403}
}

// bgBoolFalse is a deterministic rng (P19) — the TS test pins exact delays
// from rng = () => 0.75.
func bgBoolFalse() float64 { return 0.75 }

func bgFixedRng(value float64) func() float64 {
	return func() float64 { return value }
}

func TestRetryWithBackoff_429ExhaustingTheBudgetReturnsOKFalse(t *testing.T) {
	recorder := &bgRecorder{}
	opCalls := 0
	op := func(attempt int) (string, error) {
		opCalls++
		return "", bgRateLimited(429)
	}
	backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{
		MaxAttempts: 3,
		BaseDelayMs: 100,
		Rng:         bgFixedRng(0.75),
	}, recorder.delay)
	require.NoError(t, err)

	outcome, err := gitpr.RetryWithBackoff(backoff, op)

	// The 429 case returns { ok: false, lastStatus: 429 } so the CALLER
	// downgrades to report-only — the backoff itself never throws for an
	// exhausted budget.
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.Equal(t, 3, outcome.Attempts)
	require.NotNil(t, outcome.LastStatus)
	assert.Equal(t, 429, *outcome.LastStatus)
	assert.Equal(t, 3, opCalls)
	// exponential + jitter, attempt 1 → 100 + 0.75*50, attempt 2 → 200 + 0.75*50
	assert.Equal(t, []float64{137.5, 237.5}, recorder.delays)
	// no further op call, NO further delay
}

func TestRetryWithBackoff_403RetriesThenSucceeds(t *testing.T) {
	recorder := &bgRecorder{}
	opCalls := 0
	op := func(attempt int) (string, error) {
		opCalls++
		if opCalls == 1 {
			return "", bgRateLimited(403)
		}
		return "patch", nil
	}
	backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{
		MaxAttempts: 3,
		BaseDelayMs: 100,
		Rng:         bgFixedRng(0.5),
	}, recorder.delay)
	require.NoError(t, err)

	outcome, err := gitpr.RetryWithBackoff(backoff, op)

	require.NoError(t, err)
	assert.True(t, outcome.OK)
	assert.Equal(t, "patch", outcome.Value)
	assert.Equal(t, 2, outcome.Attempts)
	// one retry so far — exponential stays at the attempt-1 base + jitter
	assert.Equal(t, []float64{125}, recorder.delays)
}

func TestRetryWithBackoff_errorsThatDoNotCarry429Or403PropagateImmediately(t *testing.T) {
	recorder := &bgRecorder{}
	opCalls := 0
	boom := errors.New("boom")
	serverError := &bgRateLimitError{message: "Internal Server Error", status: 500}
	op := func(attempt int) (string, error) {
		opCalls++
		if opCalls == 1 {
			return "", boom
		}
		return "", serverError
	}
	backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{
		MaxAttempts: 3,
		BaseDelayMs: 100,
		Rng:         bgBoolFalse,
	}, recorder.delay)
	require.NoError(t, err)

	// A result that doesn't carry 429/403 propagates the error IMMEDIATELY —
	// unrelated failures are never retried into a flood.
	outcome, err := gitpr.RetryWithBackoff(backoff, op)
	require.Error(t, err)
	assert.Same(t, boom, err)
	assert.False(t, outcome.OK)
	assert.Equal(t, 1, opCalls)
	assert.Empty(t, recorder.delays) // no back-off delay at all

	// 500 is not in the retry set either.
	opCalls = 0
	op = func(attempt int) (string, error) {
		opCalls++
		return "", serverError
	}
	outcome, err = gitpr.RetryWithBackoff(backoff, op)
	require.Error(t, err)
	assert.Same(t, serverError, err)
	assert.Equal(t, 1, opCalls)
	assert.Empty(t, recorder.delays)
}

func TestCreateBackoff_configErrorsThrow(t *testing.T) {
	_, err := gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 0, BaseDelayMs: 100}, func(float64) {})
	require.Error(t, err)
	var configErr *gitpr.BackoffConfigError
	require.ErrorAs(t, err, &configErr)
	assert.Equal(t, "EDOCSYNC_BACKOFF_CONFIG", configErr.Code())

	_, err = gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 2, BaseDelayMs: 0}, func(float64) {})
	require.Error(t, err)
	require.ErrorAs(t, err, &configErr)
}

// TestRetryWithBackoff_defaultJitterIsDeterministicLCG pins the default
// jitter source: an attempt-seeded LCG, never Math.random (P19) — identical
// from two independent backoffs, and the exact JS-rendered delays.
func TestRetryWithBackoff_defaultJitterIsDeterministicLCG(t *testing.T) {
	build := func() ([]float64, []float64) {
		recorder := &bgRecorder{}
		backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 4, BaseDelayMs: 100}, recorder.delay)
		require.NoError(t, err)
		_, err = gitpr.RetryWithBackoff(backoff, func(attempt int) (string, error) {
			return "", bgRateLimited(429)
		})
		require.NoError(t, err)
		return recorder.delays, nil
	}
	first, _ := build()
	second, _ := build()
	require.Len(t, first, 3)
	assert.Equal(t, first, second, "the default jitter is deterministic across backoffs")

	// baseDelayMs * 2^(attempt-1) + lcgJitter(attempt) * (baseDelayMs / 2),
	// with the LCG values 0.18510598200373352 / 0.09164505545049906 /
	// 0.9981841288972646 for attempts 1..3 (pinned against the TS reference).
	assert.InDelta(t, 109.25529910018668, first[0], 1e-12)
	assert.InDelta(t, 204.58225277252495, first[1], 1e-12)
	assert.InDelta(t, 449.90920644486323, first[2], 1e-12)

	// strictly increasing
	assert.Greater(t, first[1], first[0])
	assert.Greater(t, first[2], first[1])
}

func TestRetryWithBackoff_delayIsStrictlyIncreasing(t *testing.T) {
	recorder := &bgRecorder{}
	backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{
		MaxAttempts: 5,
		BaseDelayMs: 100,
		Rng:         bgBoolFalse,
	}, recorder.delay)
	require.NoError(t, err)

	_, err = gitpr.RetryWithBackoff(backoff, func(attempt int) (string, error) {
		return "", bgRateLimited(429)
	})
	require.NoError(t, err)
	require.Len(t, recorder.delays, 4)
	for i := 1; i < len(recorder.delays); i++ {
		assert.Greater(t, recorder.delays[i], recorder.delays[i-1])
	}
}

// TestCreateBackoff_nonFiniteBaseDelayIsRefused pins the JS String() spelling
// of the rejected value in the message.
func TestCreateBackoff_nonFiniteBaseDelayIsRefused(t *testing.T) {
	cases := map[float64]string{
		math.NaN():   "NaN",
		math.Inf(1):  "Infinity",
		math.Inf(-1): "-Infinity",
	}
	for base, rendered := range cases {
		_, err := gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 2, BaseDelayMs: base}, func(float64) {})
		require.Error(t, err)
		var configErr *gitpr.BackoffConfigError
		require.ErrorAs(t, err, &configErr)
		assert.Equal(t, "EDOCSYNC_BACKOFF_CONFIG", configErr.Code())
		assert.Contains(t, err.Error(), "baseDelayMs must be a finite number > 0")
		assert.Contains(t, err.Error(), "got "+rendered)
	}
}

// TestBackoffConfigError_errorSurface pins the coded error surface
// (Code/Error/Unwrap) the rest of the repo matches on.
func TestBackoffConfigError_errorSurface(t *testing.T) {
	_, err := gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 0, BaseDelayMs: 1}, func(float64) {})
	require.Error(t, err)
	var configErr *gitpr.BackoffConfigError
	require.ErrorAs(t, err, &configErr)
	assert.Equal(t, "EDOCSYNC_BACKOFF_CONFIG", configErr.Code())
	assert.Contains(t, err.Error(), "EDOCSYNC_BACKOFF_CONFIG")
	assert.Contains(t, err.Error(), "maxAttempts must be an integer >= 1")
	require.Error(t, configErr.Unwrap())
	assert.Equal(t, "EDOCSYNC_BACKOFF_CONFIG", docutil.CodeOf(err))
}

// TestRetryWithBackoff_defaultDelayPrimitiveIsARealSleep pins REAL_DELAY (the
// injected-recorder default) with a 1 ms base so the suite stays fast.
func TestRetryWithBackoff_defaultDelayPrimitiveIsARealSleep(t *testing.T) {
	backoff, err := gitpr.CreateBackoff(gitpr.BackoffConfig{MaxAttempts: 2, BaseDelayMs: 1})
	require.NoError(t, err)
	start := time.Now()
	outcome, err := gitpr.RetryWithBackoff(backoff, func(attempt int) (string, error) {
		return "", bgRateLimited(429)
	})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.False(t, outcome.OK)
	assert.Equal(t, 2, outcome.Attempts)
	assert.GreaterOrEqual(t, elapsed, 500*time.Microsecond)
	assert.Less(t, elapsed, 5*time.Second)
}
