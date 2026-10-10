package gitpr

import (
	"errors"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from core/src/gitpr/client.ts — the API-410 (Gone) retry helper for
// the doc-PR write path's CREATION calls.
//
// Creation is the only idempotent-unsafe-on-410 hazard: some platforms answer
// 410 Gone for a just-created resource under eventual consistency, and the
// SAFE response is one bounded retry of the SAME creation (the marker-keyed
// upsert makes it idempotent — same key ⇒ same PR, never a duplicate, INV-11).
// READ paths (list, search, marker lookup) are NEVER wrapped: a 410 there is a
// real answer, not a race, and retrying reads is how floods happen.
//
// Contract: ONLY a rejection that structurally carries `.status === 410` (or
// `.response?.status === 410`) is retried — every other error propagates
// IMMEDIATELY, unmodified. Exhausting the budget rethrows the LAST 410 as-is
// (never swallowed, never rewrapped). No clocks of our own: the wait is a
// plain sleep for opts.DelayMs (default 1000); tests pass DelayMs: 1 (or an
// injected no-op Delay) — no real second-long sleeps.

// goneStatus is the structural status the helper retries (API-410 Gone).
const goneStatus = 410

// Default retry budget: at most two calls total (one extra attempt).
const (
	defaultGoneRetries = 1
	defaultGoneDelayMs = 1000
)

// GoneRetryOptions configures WithGoneRetry.
type GoneRetryOptions struct {
	// Retries is the number of EXTRA attempts after the first (default 1 —
	// at most two calls total).
	Retries *int
	// DelayMs is the wait between attempts in ms (default 1000).
	DelayMs *int
	// Delay overrides the wait primitive (default: a real time.Sleep). Tests
	// inject a recording no-op so no real sleeping happens (P19).
	Delay func(d time.Duration)
}

// httpStatusError is the structural `.status` rule of the TS helper: any
// thrown value with a numeric status property (no exception-class coupling —
// fetch/undici/typed errors all fit). The GitHub REST error family
// (github-rest-platform.go) satisfies it through its HTTPStatus() accessor.
type httpStatusError interface {
	HTTPStatus() int
}

// responseStatusError is the structural `.response.status` rule: the nested
// response shape some HTTP client errors carry.
type responseStatusError interface {
	ResponseHTTPStatus() int
}

// structuralStatus extracts a numeric status from err through the two
// structural probes (direct `.status` first, then the nested
// `.response.status`), mirroring statusOf/isGone of the TS modules. It is the
// ONE status probe of this package: client.ts's isGone and backoff.ts's
// statusOf share it so the 410 and 429/403 rules can never drift apart.
func structuralStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var direct httpStatusError
	if errors.As(err, &direct) {
		if status := direct.HTTPStatus(); status != 0 {
			return status, true
		}
	}
	var nested responseStatusError
	if errors.As(err, &nested) {
		if status := nested.ResponseHTTPStatus(); status != 0 {
			return status, true
		}
	}
	return 0, false
}

// isGone reports whether err structurally carries status 410 (directly or via
// its response). Error-class agnostic: only the numeric status matters.
func isGone(err error) bool {
	status, ok := structuralStatus(err)
	return ok && status == goneStatus
}

// WithGoneRetry runs fn and returns its value; on a 410 (Gone) rejection it
// waits opts.DelayMs (default 1000) and retries — at most opts.Retries
// (default 1) EXTRA attempt. Any other error propagates immediately;
// exhausting the budget returns the last 410 unmodified. Creation call sites
// only — never wrap list/search/lookup calls.
func WithGoneRetry[T any](fn func() (T, error), opts *GoneRetryOptions) (T, error) {
	retries := defaultGoneRetries
	delayMs := defaultGoneDelayMs
	var delay func(time.Duration)
	if opts != nil {
		if opts.Retries != nil {
			retries = *opts.Retries
		}
		if opts.DelayMs != nil {
			delayMs = *opts.DelayMs
		}
		delay = opts.Delay
	}
	if delay == nil {
		delay = func(d time.Duration) { time.Sleep(d) }
	}
	if retries < 0 {
		// The TS loop would exit without a verdict and `throw undefined`; Go
		// fails closed with a typed error instead of a zero value.
		var zero T
		return zero, docutil.New(docutil.CodeInternal,
			"withGoneRetry: retries must be >= 0 (fail closed)")
	}

	var lastGone error
	for attempt := 0; attempt <= retries; attempt++ {
		value, err := fn()
		if err == nil {
			return value, nil
		}
		if !isGone(err) {
			// Typed passthrough: every non-410 failure propagates IMMEDIATELY,
			// unmodified — never retried, never rewrapped.
			var zero T
			return zero, err
		}
		lastGone = err
		if attempt == retries {
			// Budget exhausted: rethrow the LAST 410 as-is (never swallowed).
			var zero T
			return zero, lastGone
		}
		delay(time.Duration(delayMs) * time.Millisecond)
	}
	// Unreachable: every iteration returns.
	var zero T
	return zero, lastGone
}
