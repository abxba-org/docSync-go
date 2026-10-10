package gitpr

// Ported from packages/core/src/gitpr/governors.ts (P6-T06 cap, P6-T07 burst,
// P6-T08 concurrency group; ARCH §12.2; invariant 11).
//
// Pure, dependency-free decision core for the Phase-6 PR write path:
//
//   - {@link OpenPrCapGovernor} (P6-T06): at/over the max-open-doc-PRs cap the
//     write path DOWNGRADES to a check-run report with the exact message
//     `patch ready, apply manually` — report-only posture, never a failed run
//     and never a queued PR.
//   - {@link HourlyBurstLimiter} (P6-T07): over the hourly budget an Admit
//     WAITS (queue-not-fail, INV-11) until the oldest in-window admission ages
//     out of the 1h window; it never rejects. Both the clock and the delay
//     primitive are injected so tests advance time without any real sleeping.
//   - {@link DocPrConcurrencyGroup} / {@link SupersedeInFlight} (P6-T08): the
//     deterministic concurrency group name `docsync-<repo>-<pr#|local>` and the
//     pure supersession decision for an in-flight run. The state write itself is
//     the caller's ledger duty (P6-T18) — this module only decides.
//
// GovernorConfigError maps to docutil's closest registered code
// (EDOCSYNC_GOVERNOR_CONFIG is not in internal/docutil; CONVENTIONS.md §Errors);
// the message text is preserved verbatim.

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// CodeGovernorConfig preserves the TS EDOCSYNC_GOVERNOR_CONFIG code verbatim
// (CONVENTIONS.md §Errors: the code is not in internal/docutil, so it rides the
// error text through docutil.New instead of inventing a new docutil code).
const CodeGovernorConfig = "EDOCSYNC_GOVERNOR_CONFIG"

// governorConfigError builds the GovernorConfigError equivalent (typed message
// preserved verbatim from the TS class).
func governorConfigError(message string) error {
	return docutil.New(CodeGovernorConfig, message)
}

/* ------------------------------------------------------- P6-T06: open-PR cap */

// PatchReadyMessage is the exact downgrade message mandated by PLAN (P6-T06 /
// ARCH §12.2) — the wording is contractual.
const PatchReadyMessage = "patch ready, apply manually"

// Hard, documented range of the configurable max-open cap (ARCH §12.2: 3,
// configurable 1-10).
const (
	OpenPrCapMin = 1
	OpenPrCapMax = 10
)

// OpenPrCapConfig is the cap governor's config (TS OpenPrCapConfig).
type OpenPrCapConfig struct {
	// MaxOpen is the max open docsync PRs per repository — an integer in [1, 10].
	MaxOpen int
}

// OpenPrCapResult is the per-call decision (TS OpenPrCapResult): `Allowed` false
// when a new doc PR must NOT be created (at/over cap) and `Downgrade` names the
// postural switch; the TS `message: string | null` is the Go empty-string
// projection (Absent ⇒ "").
type OpenPrCapResult struct {
	Allowed      bool
	Downgrade    bool
	DowngradeMsg string
}

// OpenPrCapGovernor is the P6-T06 cap governor surface.
type OpenPrCapGovernor interface {
	// Evaluate is the pure cap decision: `openCount >= maxOpen` → report-only
	// downgrade with the exact PATCH_READY_MESSAGE; strictly under cap → allowed.
	Evaluate(openCount int) (OpenPrCapResult, error)
}

// openPrCapGovernor is the report-only cap implementation.
type openPrCapGovernor struct {
	maxOpen int
}

// NewOpenPrCapGovernor (TS createOpenPrCapGovernor) validates the config and
// returns the governor. It THROWS — i.e. returns a typed error — on a
// non-integer or out-of-range cap (fail closed at construction, never at
// evaluate time; never silently coerces a misconfiguration).
func NewOpenPrCapGovernor(config OpenPrCapConfig) (OpenPrCapGovernor, error) {
	if config.MaxOpen < OpenPrCapMin || config.MaxOpen > OpenPrCapMax {
		return nil, governorConfigError("maxOpen must be an integer in [" +
			strconv.Itoa(OpenPrCapMin) + ", " + strconv.Itoa(OpenPrCapMax) +
			"] (got " + strconv.Itoa(config.MaxOpen) + ")")
	}
	return &openPrCapGovernor{maxOpen: config.MaxOpen}, nil
}

// Evaluate (TS evaluate). OVER cap is a normal, healthy state: report-only
// posture, never a crash, never a queue (an automated queue would be a backlog
// machine).
func (g *openPrCapGovernor) Evaluate(openCount int) (OpenPrCapResult, error) {
	if openCount < 0 {
		return OpenPrCapResult{}, governorConfigError(
			"openCount must be a non-negative integer (got " + strconv.Itoa(openCount) + ")")
	}
	if openCount >= g.maxOpen {
		return OpenPrCapResult{Allowed: false, Downgrade: true, DowngradeMsg: PatchReadyMessage}, nil
	}
	return OpenPrCapResult{Allowed: true, Downgrade: false, DowngradeMsg: ""}, nil
}

/* ---------------------------------------------------- P6-T07: hourly burst */

// BurstWindowMs is the fixed 1h burst window in ms (ARCH §12.2: max doc PRs per
// repository per hour).
const BurstWindowMs int64 = 3_600_000

// BurstClock is the injected millisecond clock (TS BurstClock) — the limiter
// never reads the wall clock itself (tests pass a deterministic double).
type BurstClock interface {
	NowMs() int64
}

// BurstDelay is the injected delay primitive (TS BurstDelay = (ms) =>
// Promise<void>): tests inject a fake that advances the fake clock (no real
// sleeping).
type BurstDelay func(ctx context.Context, ms int64) error

// BurstLimiterConfig is the limiter config (TS BurstLimiterConfig).
type BurstLimiterConfig struct {
	// MaxPerHour is the max doc-PR admissions per rolling 1h window per
	// repository — an integer >= 1.
	MaxPerHour int
}

// AdmitResult is one admission answer (TS AdmitResult).
type AdmitResult struct {
	Admitted     bool
	Queued       bool
	RetryAfterMs int64
}

// HourlyBurstLimiter is the P6-T07 burst limiter surface.
type HourlyBurstLimiter interface {
	// Admit is the queue-not-fail admission (INV-11): within budget → immediate
	// admit; over it → waits (via the injected delay) until the oldest
	// in-window admission ages out of the 1h window, then admits. NEVER rejects.
	Admit(ctx context.Context) (AdmitResult, error)
	// InWindowCount is the number of admissions currently counted inside the
	// rolling window (the raw ring length — TS `get inWindowCount()`).
	InWindowCount() int
}

// realBurstDelay is the TS REAL_DELAY default: a ctx-aware sleep.
func realBurstDelay(ctx context.Context, ms int64) error {
	if ms <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// hourlyBurstLimiter maintains the timestamp ring of admissions inside the
// rolling 1h window; an over-budget Admit WAITS (bounded by oldest-timestamp +
// window) and is then admitted — queue-not-fail, INV-11: the limiter never
// fails an admission and never floods. Every operation is goroutine-safe (the
// ring is shared state).
type hourlyBurstLimiter struct {
	maxPerHour int
	clock      BurstClock
	delay      BurstDelay

	mu         sync.Mutex
	admittedAt []int64
}

// NewHourlyBurstLimiter (TS createHourlyBurstLimiter) — fail closed: an invalid
// maxPerHour refuses to build. A nil delay falls back to the TS REAL_DELAY
// default (a ctx-aware sleep). Construct the limiter ONCE per repository and
// reuse it (its internal ring IS the rolling window).
func NewHourlyBurstLimiter(config BurstLimiterConfig, clock BurstClock, delay BurstDelay) (HourlyBurstLimiter, error) {
	if config.MaxPerHour < 1 {
		return nil, governorConfigError("maxPerHour must be an integer >= 1 (got " +
			strconv.Itoa(config.MaxPerHour) + ")")
	}
	if clock == nil {
		return nil, governorConfigError("createHourlyBurstLimiter: clock must be a BurstClock (injected)")
	}
	if delay == nil {
		delay = realBurstDelay
	}
	return &hourlyBurstLimiter{maxPerHour: config.MaxPerHour, clock: clock, delay: delay, admittedAt: []int64{}}, nil
}

// evictLocked drops every admission older than the window (caller holds mu).
// In-window iff ts > now - WINDOW (strict), so a timestamp exactly one window
// old has aged out.
func (l *hourlyBurstLimiter) evictLocked(now int64) {
	boundary := now - BurstWindowMs
	evict := 0
	for evict < len(l.admittedAt) && l.admittedAt[evict] <= boundary {
		evict++
	}
	if evict > 0 {
		l.admittedAt = append([]int64(nil), l.admittedAt[evict:]...)
	}
}

// Admit (TS admit) — see HourlyBurstLimiter.Admit. The TS loop (re-read the
// clock, evict, re-check after EVERY wait) is mirrored exactly; one extra
// fail-closed guard fires only when an injected delay reports NO progress at all
// (a stuck clock would otherwise spin forever).
func (l *hourlyBurstLimiter) Admit(ctx context.Context) (AdmitResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.NowMs()
	l.evictLocked(now)
	if len(l.admittedAt) < l.maxPerHour {
		l.admittedAt = append(l.admittedAt, now)
		return AdmitResult{Admitted: true, Queued: false, RetryAfterMs: 0}, nil
	}

	// Over budget → queue (never fail, INV-11). Wait until the oldest in-window
	// admission ages out; the injected delay is the ONLY wait primitive, so
	// fake-clock tests advance time with zero real sleeping.
	queuedStart := now
	for {
		l.evictLocked(now)
		if len(l.admittedAt) < l.maxPerHour {
			break
		}
		oldest := l.admittedAt[0]
		waitMs := oldest + BurstWindowMs - now
		if waitMs < 1 {
			waitMs = 1
		}
		if err := l.delay(ctx, waitMs); err != nil {
			// A ctx cancellation (or any injected delay failure) fails loud;
			// never loop, never silently admit.
			return AdmitResult{Admitted: false, Queued: true, RetryAfterMs: now - queuedStart}, err
		}
		next := l.clock.NowMs()
		if next == now && oldest == l.admittedAt[0] && len(l.admittedAt) >= l.maxPerHour {
			// No progress whatsoever: the host clock never advances — fail
			// closed instead of spinning on a hostile clock (the TS loops
			// forever; Go cannot).
			return AdmitResult{Admitted: false, Queued: true, RetryAfterMs: next - queuedStart},
				governorConfigError("hourly burst limiter: the injected delay reported no progress " +
					"(the clock never advanced); fail closed instead of queueing forever")
		}
		now = next
	}
	l.admittedAt = append(l.admittedAt, now)
	return AdmitResult{Admitted: true, Queued: true, RetryAfterMs: now - queuedStart}, nil
}

// InWindowCount (TS `get inWindowCount()`) — the raw ring length.
func (l *hourlyBurstLimiter) InWindowCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.admittedAt)
}

/* ---------------------------------------------- P6-T08: concurrency groups */

// assertValidGroupInput (TS assertValidGroupInput) — the fail-closed input
// hygiene shared by the group helpers.
func assertValidGroupInput(repo string, sourcePr *int) error {
	if len(repo) == 0 {
		return governorConfigError("repo must be a nonempty string")
	}
	if sourcePr != nil && *sourcePr < 1 {
		return governorConfigError("sourcePr must be a positive integer or null (got " +
			strconv.Itoa(*sourcePr) + ")")
	}
	return nil
}

// DocPrConcurrencyGroup (TS docPrConcurrencyGroup) — the deterministic
// concurrency-group name: `docsync-<repo>-<pr#|local>`, `local` when the run is
// not tied to a source PR. Same inputs always produce the same group; a new run
// in the same group supersedes the in-flight one (decision via
// {@link SupersedeInFlight}).
func DocPrConcurrencyGroup(repo string, sourcePr *int) (string, error) {
	if err := assertValidGroupInput(repo, sourcePr); err != nil {
		return "", err
	}
	gate := "local"
	if sourcePr != nil {
		gate = strconv.Itoa(*sourcePr)
	}
	return "docsync-" + repo + "-" + gate, nil
}

// Supersedable is the identity a run carries into the supersession decision
// (P6-T08): `idempotencyKey` is the run's (source PR head SHA, doc-set content
// hash) key; `runId` distinguishes re-runs of the same logical output.
type Supersedable struct {
	RunID          string
	IdempotencyKey string
}

// SupersedeInFlightAction is the pure supersession decision's verdict
// (TS 'superseded' | 'new').
type SupersedeInFlightAction string

// The two verdicts.
const (
	SupersedeInFlightSuperseded SupersedeInFlightAction = "superseded"
	SupersedeInFlightNew        SupersedeInFlightAction = "new"
)

// SupersedeInFlight (TS supersedeInFlight): the pure supersession decision for
// two runs in the SAME group (group routing is the caller's duty; the group is
// validated here for fail-closed hygiene):
//
//   - incoming with a DIFFERENT idempotency key (the source PR evolved) →
//     `superseded`: the in-flight run is superseded by newer analysis
//     [ARCH §12.2 stale-PR rule].
//   - incoming with the SAME idempotency key (idempotent re-run / same run
//     re-entry) → `new`: the in-flight run is NOT superseded — the re-run
//     updates the existing output in place instead of duplicating.
//
// The supersession STATE WRITE is the caller's ledger duty (P6-T18) — this
// function only decides.
func SupersedeInFlight(group string, current, incoming Supersedable) (SupersedeInFlightAction, error) {
	if err := assertValidGroupInput(group, nil); err != nil {
		return "", err
	}
	if incoming.IdempotencyKey == current.IdempotencyKey {
		return SupersedeInFlightNew, nil
	}
	return SupersedeInFlightSuperseded, nil
}
