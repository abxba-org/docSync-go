package gitpr

import (
	"math"
	"strconv"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from core/src/gitpr/backoff.ts — 429/403 backoff with report-only
// fallback (P6-T09; ARCH §12.2 secondary-rate rule; INV 11 no-flood).
//
// Exponential delay (`baseDelayMs * 2^(attempt-1)`) plus jitter, retrying ONLY
// errors that structurally carry `.status` 429 or 403. Anything else propagates
// IMMEDIATELY — unrelated failures are never retried into a flood. Attempts are
// hard-capped at MaxAttempts; exhaustion returns an ok:false outcome and the
// CALLER downgrades to report-only (the backoff itself never creates reports,
// never retries past the cap, and never fails a run by returning an error for
// an exhausted budget).
//
// Jitter is injectable; the DEFAULT is a deterministic LCG seeded from the
// attempt number — Math.random is never used (P19-pure, reproducible runs).
// The delay primitive is injectable; the default is a real sleep — tests
// inject a recording no-op so no real sleeping happens.

// CodeBackoffConfig preserves the TS EDOCSYNC_BACKOFF_CONFIG code verbatim.
// docutil has no backoff code yet, so — following the globs/statemachine
// precedent of carrying the TS code through docutil.New — the constant lives
// here and no new docutil code is invented (CONVENTIONS.md §Errors).
const CodeBackoffConfig = "EDOCSYNC_BACKOFF_CONFIG"

// BackoffConfigError is the typed range error for invalid backoff
// configuration (fail closed at construction; the TS class extends
// RangeError, preserved through the error's Code()).
type BackoffConfigError struct {
	coded *docutil.Error
}

// Code reports the machine-readable code (EDOCSYNC_BACKOFF_CONFIG).
func (e *BackoffConfigError) Code() string { return CodeBackoffConfig }

// Error renders the coded error in docutil house style.
func (e *BackoffConfigError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf resolves the code.
func (e *BackoffConfigError) Unwrap() error { return e.coded }

// newBackoffConfigError builds the typed configuration refusal.
func newBackoffConfigError(message string) *BackoffConfigError {
	return &BackoffConfigError{coded: docutil.New(CodeBackoffConfig, message)}
}

/* ------------------------------------------------------------------- config */

// BackoffConfig configures a Backoff.
type BackoffConfig struct {
	// MaxAttempts is the hard cap on op invocations — integer >= 1 (the
	// backoff NEVER exceeds it).
	MaxAttempts int
	// BaseDelayMs is the base delay in ms; attempt n waits
	// `BaseDelayMs * 2^(n-1) + jitter`.
	BaseDelayMs float64
	// Rng is the injectable uniform [0,1) source; the default is a
	// deterministic attempt-seeded LCG.
	Rng func() float64
}

// BackoffDelay is the injectable delay primitive (default: a real sleep;
// tests inject a recording no-op).
type BackoffDelay func(ms float64)

// BackoffOutcome is the discriminated result of RetryWithBackoff:
//   - OK true: Value carries the op's result and Attempts the invocation count.
//   - OK false: the budget was EXHAUSTED — the caller downgrades to
//     report-only. LastStatus carries the final rate-limit error's status.
type BackoffOutcome[T any] struct {
	OK         bool
	Value      T
	Attempts   int
	LastStatus *int
}

// Backoff is the P6-T09 backoff surface.
type Backoff struct {
	maxAttempts int
	baseDelayMs float64
	rng         func() float64
	delay       BackoffDelay
}

// The only statuses the backoff retries (secondary-rate family).
const (
	statusTooManyRequests = 429
	statusForbidden       = 403
)

// isRateLimitStatus reports membership in the retry set.
func isRateLimitStatus(status int) bool {
	return status == statusTooManyRequests || status == statusForbidden
}

// realBackoffDelay is the default delay primitive (the TS REAL_DELAY).
func realBackoffDelay(ms float64) {
	if ms > 0 {
		time.Sleep(time.Duration(ms * float64(time.Millisecond)))
	}
}

// backoffNumberText renders a number the way JS String() does for the
// magnitudes a delay configuration can carry (no exponent below 1e21).
func backoffNumberText(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// lcgJitter is the deterministic default jitter: an LCG (Numerical Recipes
// constants) seeded from the attempt number — never Math.random, reproducible
// across runs. The uint32 arithmetic wraps exactly like the TS
// Math.imul/`>>> 0` pipeline.
func lcgJitter(attempt int) float64 {
	// Nonzero 32-bit seed mixed from the attempt (Knuth multiplicative hash).
	s := uint32(int64(attempt) * 2654435761)
	s = s*1664525 + 1013904223
	s = s*1664525 + 1013904223
	return float64(s) / 4294967296
}

// CreateBackoff builds the P6-T09 backoff. MaxAttempts must be >= 1 and
// BaseDelayMs a finite number > 0 — otherwise a typed BackoffConfigError
// (fail closed before any retry loop exists).
func CreateBackoff(config BackoffConfig, delay ...BackoffDelay) (*Backoff, error) {
	if config.MaxAttempts < 1 {
		// TS also rejects non-integers; a Go int is always integral, so only
		// the range arm is expressible here.
		return nil, newBackoffConfigError(
			"maxAttempts must be an integer >= 1 (got " + strconv.Itoa(config.MaxAttempts) + ")")
	}
	if math.IsNaN(config.BaseDelayMs) || math.IsInf(config.BaseDelayMs, 0) || config.BaseDelayMs <= 0 {
		return nil, newBackoffConfigError(
			"baseDelayMs must be a finite number > 0 (got " + backoffNumberText(config.BaseDelayMs) + ")")
	}
	backoff := &Backoff{
		maxAttempts: config.MaxAttempts,
		baseDelayMs: config.BaseDelayMs,
		rng:         config.Rng,
		delay:       realBackoffDelay,
	}
	for _, candidate := range delay {
		if candidate != nil {
			backoff.delay = candidate
		}
	}
	return backoff, nil
}

// RetryWithBackoff runs op (receiving the 1-based attempt number) under the
// exponential + jitted backoff. A 429/403 rejection backs off and retries up
// to MaxAttempts; exhaustion yields an ok:false outcome for the caller's
// report-only downgrade. Every other error propagates IMMEDIATELY — never
// backed off, never swallowed.
func RetryWithBackoff[T any](b *Backoff, op func(attempt int) (T, error)) (BackoffOutcome[T], error) {
	for attempt := 1; attempt <= b.maxAttempts; attempt++ {
		value, err := op(attempt)
		if err == nil {
			return BackoffOutcome[T]{OK: true, Value: value, Attempts: attempt}, nil
		}
		status, hasStatus := structuralStatus(err)
		if !hasStatus || !isRateLimitStatus(status) {
			// Typed passthrough: non-429/403 failures propagate IMMEDIATELY.
			var zero BackoffOutcome[T]
			return zero, err
		}
		if attempt == b.maxAttempts {
			// Attempts exhausted → report-only fallback: ok:false, no further
			// op call, NO further delay (the backoff never floods, INV 11).
			last := status
			return BackoffOutcome[T]{OK: false, Attempts: attempt, LastStatus: &last}, nil
		}
		jitterSource := b.rng
		if jitterSource == nil {
			seeded := attempt
			jitterSource = func() float64 { return lcgJitter(seeded) }
		}
		exponential := b.baseDelayMs * math.Pow(2, float64(attempt-1))
		jitter := jitterSource() * (b.baseDelayMs / 2)
		b.delay(exponential + jitter)
	}
	// Unreachable: every iteration returns.
	var zero BackoffOutcome[T]
	return zero, newBackoffConfigError("retry loop exited without a verdict")
}
