// Ported from packages/policy/src/shadow/live.ts (P12-T02; ARCH §8.6/§7.1; D6; OD-R29):
// the sampled live-shadow runner — the SECONDARY shadow lane per D6, beside
// the primary offline historical replay (replay.go), protecting Loop A latency
// and CI budget; dual-Jev on every PR is explicitly rejected.
//
// Sampling (deterministic, no randomness): the draw is the first 8 hex chars
// of sha256(run_id ⊕ NUL ⊕ pr_id) compared against a fraction-derived
// threshold — the same (run id, PR) pair always draws the same value, so tests
// pin outcomes instead of seeding randomness. The fraction is VERSIONED POLICY
// DATA passed by the caller; the shipped default is the OD-R29 RECORDED
// RECOMMENDATION adopted as recorded data (10%; sampling applies ONLY to repos
// opted into learning.mode=canary|auto — ineligible repos are NEVER sampled,
// the runner fails the eligibility gate before the draw).
//
// Shadow decisions are recorded into docsync.shadow-run-record/v1 records
// marked shadow_only: true and are NEVER surfaced to developers: the runner
// returns the caller's developer-visible output UNTOUCHED (same reference,
// byte-identical JSON — V-P12-1 zero-effect contract) and the shadow decision
// appears only inside the shadow record. The decision path is an INJECTED
// port (recorded double in tests — no network, no OpenRouter calls).
//
// Time-boxing (ARCH §8.6 "time-box canaries; auto-expire stale shadows"): every
// record carries created_at (the injected instant — never a clock) and
// expires_at (created_at + maxAge). Lifecycle enforcement (auto-expiry of
// stale shadows) is P12-T05; this runner records the expiry data and exposes
// IsShadowRecordExpired.
//
// Dependency note: the LearningMode vocabulary (governance/learning-mode.ts)
// is declared by learning-mode.go in this package — reused here, never
// redefined.
package policy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// DefaultSampledShadowFraction is the OD-R29 recorded-recommendation default
// fraction (adopted as recorded data; versioned policy data in production —
// callers pass it from policy-version data). FLAG-FOR-USER: OD-R29 remains an
// open decision in PLAN §13.6.
const DefaultSampledShadowFraction = 0.1

// DefaultShadowMaxAgeDays is the time-box default (days) — time-boxed canaries
// (ARCH §8.6); enforcement is P12-T05.
const DefaultShadowMaxAgeDays = 14

// shadowDrawSpace is the 2^32 deterministic-draw space
// (TS `fraction * 2 ** 32`).
const shadowDrawSpace = uint64(1) << 32

// shadowCrockfordAlphabet is the Crockford base32 alphabet (no I, L, O, U) the
// deterministic record id is mapped onto (ULID shape).
const shadowCrockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// shadowInstantLayout renders an instant the way JS `Date.prototype.toISOString`
// does — always millisecond precision, always 'Z'.
const shadowInstantLayout = "2006-01-02T15:04:05.000Z"

/* ------------------------------------------------------------ typed errors */

// LiveShadowConfigError is the fail-closed runner-configuration defect,
// thrown before any PR is considered (TS LiveShadowConfigError).
type LiveShadowConfigError struct {
	Message string

	inner *docutil.Error
}

func newLiveShadowConfigError(message string) *LiveShadowConfigError {
	return &LiveShadowConfigError{Message: message, inner: docutil.New(docutil.CodeConfig, message)}
}

// Error renders the coded error (EDOCSYNC_CONFIG: <message>).
func (e *LiveShadowConfigError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_CONFIG.
func (e *LiveShadowConfigError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *LiveShadowConfigError) Unwrap() error { return e.inner }

// ShadowLiveInputError is the fail-closed violation for a malformed
// consider-turn input (TS ShadowLiveInputError).
type ShadowLiveInputError struct {
	Message string

	inner *docutil.Error
}

func newShadowLiveInputError(message string) *ShadowLiveInputError {
	return &ShadowLiveInputError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowLiveInputError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowLiveInputError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowLiveInputError) Unwrap() error { return e.inner }

// ShadowLivePortError is the fail-closed violation: the injected
// shadow-decision port returned an invalid decision (TS ShadowLivePortError).
type ShadowLivePortError struct {
	Message string

	inner *docutil.Error
}

func newShadowLivePortError(message string) *ShadowLivePortError {
	return &ShadowLivePortError{Message: message, inner: docutil.New(docutil.CodeValidation, message)}
}

// Error renders the coded error (EDOCSYNC_VALIDATION: <message>).
func (e *ShadowLivePortError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_VALIDATION.
func (e *ShadowLivePortError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowLivePortError) Unwrap() error { return e.inner }

// ShadowLiveRecordError is the fail-closed violation: the assembled shadow
// record failed its registered schema (TS ShadowLiveRecordError).
type ShadowLiveRecordError struct {
	Message string

	inner *docutil.Error
}

func newShadowLiveRecordError(message string) *ShadowLiveRecordError {
	return &ShadowLiveRecordError{Message: message, inner: docutil.New(docutil.CodeSchema, message)}
}

// Error renders the coded error (EDOCSYNC_SCHEMA: <message>).
func (e *ShadowLiveRecordError) Error() string { return e.inner.Error() }

// Code returns EDOCSYNC_SCHEMA.
func (e *ShadowLiveRecordError) Code() string { return e.inner.Code }

// Unwrap exposes the coded error for errors.As/Is on *docutil.Error.
func (e *ShadowLiveRecordError) Unwrap() error { return e.inner }

/* -------------------------------------------------------------------- types */

// LiveShadowDecision is the recorded shadow decision — the candidate policy's
// decision, shadow-only (TS LiveShadowDecision).
type LiveShadowDecision struct {
	Action        schemas.Action
	Abstained     bool
	AbstainReason *schemas.AbstainReason
}

// LiveShadowPortInput is the injected port's input (TS LiveShadowPort's
// anonymous object).
type LiveShadowPortInput struct {
	PrID   string
	Inputs ShadowReplayInputs
}

// LiveShadowPort is the injected shadow-decision port (the recorded double in
// tests): the candidate policy's live decision path for one sampled PR. D6
// memoization separation is inherited upstream — shadow-side decision caching
// runs under the `shadow:<binding>` memo namespace, never production
// (ARCH §7.1).
type LiveShadowPort func(input LiveShadowPortInput) LiveShadowDecision

// LiveShadowRunnerConfig is the runner configuration
// (TS LiveShadowRunnerConfig). Fraction and MaxAgeDays are optional (nil =
// the TS `undefined`; the documented default is applied).
type LiveShadowRunnerConfig struct {
	// RunID is the live-shadow run id — part of the deterministic sampling key.
	RunID string
	Repo  string
	// LearningMode is the repo learning mode — the OD-R29 eligibility gate.
	LearningMode            LearningMode
	ProductionPolicyVersion int
	CandidatePolicyVersion  int
	// Fraction is the sample fraction — versioned policy data; nil defaults to
	// DefaultSampledShadowFraction.
	Fraction *float64
	// MaxAgeDays is the time-box in days; nil defaults to
	// DefaultShadowMaxAgeDays.
	MaxAgeDays *int
	// Now is the injected instant (ISO-8601) anchoring created_at — never a
	// clock.
	Now                 string
	CandidateShadowPort LiveShadowPort
}

// LiveShadowSamplingDecision is the deterministic sampling outcome for one PR
// — fully recorded for auditability (TS LiveShadowSamplingDecision).
type LiveShadowSamplingDecision struct {
	Sampled      bool
	Eligible     bool
	Fraction     float64
	DrawHex      string
	ThresholdHex string
	Reason       string
}

// LiveShadowConsiderInput is the considerTurn input
// (TS LiveShadowConsiderInput<TDeveloperOutput>). A nil Inputs means the TS
// `undefined` — legal only when the PR is not sampled.
type LiveShadowConsiderInput[T any] struct {
	PrID string
	// DeveloperOutput is the developer-visible output — returned UNTOUCHED
	// (V-P12-1 zero effect).
	DeveloperOutput T
	// Inputs are the shadow inputs (required when the PR is sampled).
	Inputs *ShadowReplayInputs
}

// LiveShadowTurn is the considerTurn outcome
// (TS LiveShadowTurn<TDeveloperOutput>).
type LiveShadowTurn[T any] struct {
	// DeveloperOutput is the developer-visible output — byte-identical with
	// shadow off.
	DeveloperOutput T
	// ShadowRecord is shadow-only; NEVER surfaced to developers. Present only
	// when sampled.
	ShadowRecord *schemas.ShadowRunRecord
}

// SampledLiveShadowRunner is the pure, deterministic runner
// (TS SampledLiveShadowRunner): no clocks, no randomness, no network.
type SampledLiveShadowRunner[T any] struct {
	// Exported surface (the TS readonly props).
	Eligible   bool
	Fraction   float64
	MaxAgeDays int
	CreatedAt  string
	ExpiresAt  string

	runID                   string
	repo                    string
	learningMode            LearningMode
	productionPolicyVersion int
	candidatePolicyVersion  int
	threshold               uint64
	thresholdHex            string
	port                    LiveShadowPort
}

/* ---------------------------------------------------------------- functions */

// shadowLearningModeNames lists the closed learning.mode vocabulary in
// canonical order (LEARNING_MODE_VALUES.join(' | ') parity).
func shadowLearningModeNames() []string {
	names := make([]string, len(LearningModeValues))
	for i, mode := range LearningModeValues {
		names[i] = string(mode)
	}
	return names
}

// canonicalShadowInstant parses a canonical ISO-8601 instant the way
// `new Date(value).toISOString() === value` does: millisecond precision, 'Z'
// only, real calendar date (TS createLiveShadowRunner's now check).
func canonicalShadowInstant(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	if parsed.UTC().Format(shadowInstantLayout) != value {
		return time.Time{}, false
	}
	return parsed, true
}

// formatShadowFloat renders a float the way JS `String(value)` does for the
// fail-closed config messages (shortest round-trip form).
func formatShadowFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// deterministicShadowRecordID is the deterministic record id: sha256(seed)
// mapped onto the Crockford base32 alphabet (ULID shape)
// (TS deterministicShadowRecordId).
func deterministicShadowRecordID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	var id strings.Builder
	for index := 0; index < 26; index++ {
		id.WriteByte(shadowCrockfordAlphabet[digest[index]%32])
	}
	return id.String()
}

// recordedShadowInputHashes records the component input hashes for V-P12-2
// lineage (TS recordedInputHashes). The validation pass (canonical
// ChangeAnalysis + candidate set + diff-subset hash) fails closed before any
// hash is recorded.
func recordedShadowInputHashes(inputs ShadowReplayInputs) (*schemas.ShadowRecordedInputHashes, error) {
	if _, err := ShadowReplayInputHash(inputs); err != nil {
		return nil, err
	}
	changeAnalysis, err := shadowCanonicalJSON(inputs.ChangeAnalysis)
	if err != nil {
		return nil, err
	}
	candidateSet := append([]string(nil), inputs.CandidateSet...)
	sort.Strings(candidateSet)
	candidateSetCanonical, err := shadowCanonicalJSON(candidateSet)
	if err != nil {
		return nil, err
	}
	return &schemas.ShadowRecordedInputHashes{
		ChangeAnalysis: docutil.ContentHash(changeAnalysis),
		CandidateSet:   docutil.ContentHash(candidateSetCanonical),
		DiffSubset:     inputs.DiffSubsetHash,
	}, nil
}

// parseRegisteredShadowRunRecord is the Go form of
// SHADOW_RUN_RECORD_SCHEMA.safeParse: strict wire decode + validation
// (unknown JSON keys rejected, mirroring z.strictObject).
func parseRegisteredShadowRunRecord(record schemas.ShadowRunRecord) (*schemas.ShadowRunRecord, error) {
	wire, err := json.Marshal(record)
	if err != nil {
		return nil, docutil.New(docutil.CodeInternal,
			fmt.Sprintf("shadow run record: canonical wire marshal failed: %s", err))
	}
	return schemas.ParseShadowRunRecord(wire)
}

// CreateLiveShadowRunner creates the sampled live-shadow runner (P12-T02). All
// configuration is validated up front (fail closed, LiveShadowConfigError);
// the returned runner is pure and deterministic — no clocks, no randomness, no
// network (the decision path is the injected port).
//
// The type parameter mirrors the TS generic considerTurn<TDeveloperOutput>:
// the developer-visible output flows through untouched (V-P12-1).
func CreateLiveShadowRunner[T any](config LiveShadowRunnerConfig) (*SampledLiveShadowRunner[T], error) {
	if len(config.RunID) == 0 {
		return nil, newLiveShadowConfigError("createLiveShadowRunner: runId must be a non-empty string")
	}
	if len(config.Repo) == 0 {
		return nil, newLiveShadowConfigError("createLiveShadowRunner: repo must be a non-empty string")
	}
	if !IsValidLearningMode(string(config.LearningMode)) {
		return nil, newLiveShadowConfigError(fmt.Sprintf(
			"createLiveShadowRunner: learningMode %s is not a learning.mode value (%s)",
			jsonStringify(string(config.LearningMode)), strings.Join(shadowLearningModeNames(), " | ")))
	}
	if config.ProductionPolicyVersion <= 0 {
		return nil, newLiveShadowConfigError("createLiveShadowRunner: productionPolicyVersion must be a positive integer")
	}
	if config.CandidatePolicyVersion <= 0 {
		return nil, newLiveShadowConfigError("createLiveShadowRunner: candidatePolicyVersion must be a positive integer")
	}
	if config.ProductionPolicyVersion == config.CandidatePolicyVersion {
		return nil, newLiveShadowConfigError(
			"createLiveShadowRunner: production and candidate policy versions must differ — a shadow run compares two policies")
	}
	fraction := DefaultSampledShadowFraction
	if config.Fraction != nil {
		fraction = *config.Fraction
	}
	if math.IsNaN(fraction) || fraction < 0 || fraction > 1 {
		return nil, newLiveShadowConfigError(fmt.Sprintf(
			"createLiveShadowRunner: fraction must be a number in [0, 1] (got %s)", formatShadowFloat(fraction)))
	}
	maxAgeDays := DefaultShadowMaxAgeDays
	if config.MaxAgeDays != nil {
		maxAgeDays = *config.MaxAgeDays
	}
	if maxAgeDays <= 0 {
		return nil, newLiveShadowConfigError(fmt.Sprintf(
			"createLiveShadowRunner: maxAgeDays must be a positive integer (got %d)", maxAgeDays))
	}
	createdAt, ok := canonicalShadowInstant(config.Now)
	if !ok {
		return nil, newLiveShadowConfigError(fmt.Sprintf(
			"createLiveShadowRunner: now must be a canonical ISO-8601 instant (got %s)", config.Now))
	}
	expiresAt := createdAt.Add(time.Duration(maxAgeDays) * 24 * time.Hour)
	// OD-R29 recorded recommendation (adopted as recorded data — see module
	// docs): sampled live shadow ONLY for repos opted into canary/auto.
	eligible := IsLearningModeEscalation(config.LearningMode)
	threshold := uint64(math.Round(fraction * float64(shadowDrawSpace)))
	if threshold > shadowDrawSpace {
		threshold = shadowDrawSpace
	}
	return &SampledLiveShadowRunner[T]{
		Eligible:                eligible,
		Fraction:                fraction,
		MaxAgeDays:              maxAgeDays,
		CreatedAt:               createdAt.UTC().Format(shadowInstantLayout),
		ExpiresAt:               expiresAt.UTC().Format(shadowInstantLayout),
		runID:                   config.RunID,
		repo:                    config.Repo,
		learningMode:            config.LearningMode,
		productionPolicyVersion: config.ProductionPolicyVersion,
		candidatePolicyVersion:  config.CandidatePolicyVersion,
		threshold:               threshold,
		thresholdHex:            fmt.Sprintf("%08x", threshold),
		port:                    config.CandidateShadowPort,
	}, nil
}

// ShouldSample returns the deterministic sampling decision for one PR
// (TS SampledLiveShadowRunner.shouldSample).
func (r *SampledLiveShadowRunner[T]) ShouldSample(prID string) (LiveShadowSamplingDecision, error) {
	if len(prID) == 0 {
		return LiveShadowSamplingDecision{}, newShadowLiveInputError("shouldSample: prId must be a non-empty string")
	}
	drawHex := docutil.ContentHash(r.runID + "\x00" + prID)
	if len(drawHex) > 8 {
		drawHex = drawHex[:8]
	}
	draw, err := strconv.ParseUint(drawHex, 16, 64)
	if err != nil {
		// Unreachable: drawHex is 8 lowercase hex chars from a sha256 digest.
		return LiveShadowSamplingDecision{}, newShadowLiveInputError(fmt.Sprintf(
			"shouldSample: deterministic draw %s is not a hex value", drawHex))
	}
	sampled := r.Eligible && draw < r.threshold
	var reason string
	switch {
	case !r.Eligible:
		reason = fmt.Sprintf(
			"learning.mode=%s is not canary|auto — sampled live shadow is ineligible (OD-R29 recorded recommendation, adopted as recorded data)",
			r.learningMode)
	case sampled:
		reason = "sampled: deterministic draw below the fraction threshold"
	default:
		reason = "sampling draw at or above the fraction threshold"
	}
	return LiveShadowSamplingDecision{
		Sampled:      sampled,
		Eligible:     r.Eligible,
		Fraction:     r.Fraction,
		DrawHex:      drawHex,
		ThresholdHex: r.thresholdHex,
		Reason:       reason,
	}, nil
}

// ConsiderTurn runs one turn beside production (TS
// SampledLiveShadowRunner.considerTurn). The V-P12-1 zero-effect contract: the
// developer-visible output is passed through UNTOUCHED — no annotation, no
// wrapping, no shadow fields.
func (r *SampledLiveShadowRunner[T]) ConsiderTurn(input LiveShadowConsiderInput[T]) (LiveShadowTurn[T], error) {
	if len(input.PrID) == 0 {
		return LiveShadowTurn[T]{}, newShadowLiveInputError("considerTurn: prId must be a non-empty string")
	}
	sampling, err := r.ShouldSample(input.PrID)
	if err != nil {
		return LiveShadowTurn[T]{}, err
	}
	if !sampling.Sampled {
		return LiveShadowTurn[T]{DeveloperOutput: input.DeveloperOutput}, nil
	}
	if input.Inputs == nil {
		return LiveShadowTurn[T]{}, newShadowLiveInputError(fmt.Sprintf(
			"considerTurn: PR %s was sampled but no shadow inputs were provided — a sampled shadow turn cannot record its decision without the identical-input lineage (V-P12-2)",
			input.PrID))
	}
	decision := r.port(LiveShadowPortInput{PrID: input.PrID, Inputs: *input.Inputs})
	if !schemas.IsValidAction(string(decision.Action)) {
		return LiveShadowTurn[T]{}, newShadowLivePortError(fmt.Sprintf(
			"candidateShadowPort returned decision action %s for PR %s — outside the closed docsync.decision/v1 action vocabulary (fail closed)",
			jsonStringify(string(decision.Action)), input.PrID))
	}
	inputHashes, err := recordedShadowInputHashes(*input.Inputs)
	if err != nil {
		return LiveShadowTurn[T]{}, err
	}
	record := schemas.ShadowRunRecord{
		Schema:     schemas.ShadowRunRecordSchemaID,
		RecordID:   deterministicShadowRecordID(r.runID + "\x00" + input.PrID),
		RunID:      r.runID,
		PrID:       input.PrID,
		Repo:       r.repo,
		ShadowOnly: true,
		Sampled:    true,
		Eligible:   sampling.Eligible,
		Sampling: schemas.ShadowSampling{
			Sampler:      schemas.ShadowSamplerSHA256HexThreshold,
			Fraction:     sampling.Fraction,
			DrawHex:      sampling.DrawHex,
			ThresholdHex: sampling.ThresholdHex,
		},
		Policy: schemas.ShadowRecordedPolicy{
			ProductionVersion: r.productionPolicyVersion,
			CandidateVersion:  r.candidatePolicyVersion,
		},
		Decision: &schemas.ShadowDecision{
			Action:        decision.Action,
			Abstained:     decision.Abstained,
			AbstainReason: decision.AbstainReason,
		},
		InputHashes: inputHashes,
		CreatedAt:   r.CreatedAt,
		ExpiresAt:   r.ExpiresAt,
	}
	parsed, err := parseRegisteredShadowRunRecord(record)
	if err != nil {
		return LiveShadowTurn[T]{}, newShadowLiveRecordError(fmt.Sprintf(
			"assembled shadow record for PR %s failed docsync.shadow-run-record/v1: %s", input.PrID, err))
	}
	return LiveShadowTurn[T]{DeveloperOutput: input.DeveloperOutput, ShadowRecord: parsed}, nil
}

// IsShadowRecordExpired is the expiry probe for the time-box (ARCH §8.6):
// `now` at or after `expires_at` means the shadow is stale. Enforcement
// (auto-expiry) is P12-T05 — this is the lifecycle data seam the enforcer
// consumes (TS isShadowRecordExpired).
func IsShadowRecordExpired(record schemas.ShadowRunRecord, now string) (bool, error) {
	nowMs, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return false, newShadowLiveInputError(fmt.Sprintf(
			"isShadowRecordExpired: now is not a parseable instant: %s", now))
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	if err != nil {
		// TS: Date.parse(expires_at) is NaN and `nowMs >= NaN` is false. The
		// record schema validates expires_at, so this is unreachable in
		// practice — parity with the TS comparison, not a silent downgrade.
		return false, nil
	}
	return !nowMs.Before(expiresAt), nil
}
