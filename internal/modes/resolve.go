package modes

import (
	"math"
	"strconv"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ModeLadder is the permissiveness ladder over the five ARCH §11.2 modes;
// higher = more permissive (TS MODE_LADDER). Treat it as read-only: it is a
// package-level table, not per-call state.
//
// `human` ranks equal to `suggest` because §11.2 gives human-owned documents
// the same review-gated treatment (AI may propose, a human merges); there is no
// rung "more permissive than suggest but still gated". `locked` and `generated`
// rank 0 because they are not ladder rungs at all but non-writable CLASSES:
// they are excluded from the patch pipeline outright (enforcement
// `locked-floor` / `generated-excluded`), not merely gated, so no comparison
// "between rungs" is meaningful for them.
var ModeLadder = map[schemas.DocMode]int{
	schemas.DocModeAuto:      3,
	schemas.DocModeSuggest:   2,
	schemas.DocModeHuman:     2,
	schemas.DocModeLocked:    0,
	schemas.DocModeGenerated: 0,
}

// IsMorePermissive reports whether mode a is strictly more permissive than mode
// b on ModeLadder.
func IsMorePermissive(a, b schemas.DocMode) bool {
	return ModeLadder[a] > ModeLadder[b]
}

// OwnershipCategory is an independent ownership category (ARCH §11.2) —
// orthogonal to the modes.
type OwnershipCategory string

// Ownership categories (TS OWNERSHIP_CATEGORIES, in canonical order).
const (
	OwnershipHumanAuthored OwnershipCategory = "human-authored"
	OwnershipAIMaintained  OwnershipCategory = "ai-maintained"
	OwnershipGenerated     OwnershipCategory = "generated"
	OwnershipExternal      OwnershipCategory = "external"
	OwnershipLocked        OwnershipCategory = "locked"
)

// OwnershipCategories lists the independent ownership categories in canonical
// order (TS OWNERSHIP_CATEGORIES).
var OwnershipCategories = []OwnershipCategory{
	OwnershipHumanAuthored,
	OwnershipAIMaintained,
	OwnershipGenerated,
	OwnershipExternal,
	OwnershipLocked,
}

func isKnownOwnership(category OwnershipCategory) bool {
	for _, known := range OwnershipCategories {
		if known == category {
			return true
		}
	}
	return false
}

// ModeResolutionInput is one document's mode-resolution input. Optional fields
// are pointers: nil means "absent" (TS `undefined`).
type ModeResolutionInput struct {
	// EntryMode is the mode from the per-document manifest entry (precedence
	// layer 2).
	EntryMode *schemas.DocMode
	// DefaultsMode is the mode from the manifest `defaults:` block (precedence
	// layer 3).
	DefaultsMode *schemas.DocMode
	// Ownership is the detected/declared ownership (may be absent). Floors
	// dominate modes (D2).
	Ownership *OwnershipCategory
	// ClassifierConfidence is present ONLY so this module can demonstrably
	// ignore it for floor decisions (D2): it never upgrades a
	// locked/generated/external classification and never influences any other
	// output field (V-P0-5 contract). Must be a finite number in [0, 1].
	ClassifierConfidence *float64
}

// ModeResolution is the resolved mutability posture for one document.
type ModeResolution struct {
	// Mode is the effective mutability mode after ownership floors.
	Mode schemas.DocMode
	// Ownership is the ownership used for this resolution, or nil when none
	// was given (TS null).
	Ownership *OwnershipCategory
	// Writable reports whether AI may ever produce/apply content for this
	// document at all.
	Writable bool
	// PatchCandidate reports whether the document enters the patch candidate
	// pipeline (auto/suggest/human only).
	PatchCandidate bool
	// AutoApply reports whether the document may be auto-committed/applied —
	// only ever true for mode "auto".
	AutoApply bool
	// BlockedReason explains why the document is NOT a patch candidate; empty
	// when it is one (TS null → empty string).
	BlockedReason string
	// Enforcement names the mechanism that enforced the decision: an ownership
	// floor (D2), a generated/external exclusion, or the explicit mode
	// selection. Empty for the pure built-in default (no entry, no defaults, no
	// ownership override) — TS null → empty string.
	Enforcement string
}

// Enforcement values (TS ModeResolution['enforcement'] union).
const (
	EnforcementLockedFloor       = "locked-floor"
	EnforcementGeneratedExcluded = "generated-excluded"
	EnforcementExternalExcluded  = "external-excluded"
	EnforcementMode              = "mode"
)

// assertValidInput fails closed on malformed inputs (D8): reject, never guess.
func assertValidInput(input ModeResolutionInput) error {
	if input.Ownership != nil && !isKnownOwnership(*input.Ownership) {
		return &ModeConflictError{
			Message: `unknown ownership category "` + string(*input.Ownership) + `"`,
		}
	}
	if confidence := input.ClassifierConfidence; confidence != nil {
		if math.IsNaN(*confidence) || math.IsInf(*confidence, 0) || *confidence < 0 || *confidence > 1 {
			return &ModeConflictError{
				Message: "classifierConfidence must be a finite number in [0, 1], got " + jsStringOf(*confidence),
			}
		}
	}
	return nil
}

// jsStringOf renders a float the way the TS String(confidence) did in the
// original message (NaN/Infinity included).
func jsStringOf(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "Infinity"
	case math.IsInf(value, -1):
		return "-Infinity"
	default:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
}

const lockedBlockedReason = "mode 'locked' is terminal: no confidence, flag, or policy layer overrides it — only a human PR editing manifest.yml can change it"

// resolveDocMode is the effective mode from entry ?? defaults ?? built-in
// "suggest" (ARCH §11.3).
func resolveDocMode(input ModeResolutionInput) schemas.DocMode {
	if input.EntryMode != nil {
		return *input.EntryMode
	}
	if input.DefaultsMode != nil {
		return *input.DefaultsMode
	}
	return schemas.DocModeSuggest
}

func notPatchCandidate(
	mode schemas.DocMode,
	ownership *OwnershipCategory,
	enforcement, blockedReason string,
) ModeResolution {
	return ModeResolution{
		Mode:           mode,
		Ownership:      ownership,
		Writable:       false,
		PatchCandidate: false,
		AutoApply:      false,
		BlockedReason:  blockedReason,
		Enforcement:    enforcement,
	}
}

const generatedBlockedReason = "ownership 'generated': excluded from patch candidates — drift is observed and reported, the AI never fights another generator (ARCH §11.2)"

const externalBlockedReason = "ownership 'external': excluded from patch candidates — the document is owned outside this repository (ARCH §11.2)"

// ResolveEffectiveMode resolves the effective mutability mode and
// patch-pipeline posture for one document. Pure: same input → deep-equal
// output, for ANY confidence value.
//
// Malformed inputs fail closed with a ModeConflictError (docutil.CodeConfig):
// the ownership category must be one of OwnershipCategories and
// ClassifierConfidence must be a finite number in [0, 1].
func ResolveEffectiveMode(input ModeResolutionInput) (ModeResolution, error) {
	if err := assertValidInput(input); err != nil {
		return ModeResolution{}, err
	}

	// D2 — ownership is the floor and dominates every mode source and every
	// confidence value. `locked` additionally forces the mode itself so the
	// blocked state is visible in `Mode`, not only in the flags.
	if input.Ownership != nil {
		ownership := *input.Ownership
		switch ownership {
		case OwnershipLocked:
			return notPatchCandidate(
				schemas.DocModeLocked,
				&ownership,
				EnforcementLockedFloor,
				"ownership category 'locked' is a floor (D2): classifier confidence never overrides it (0.99 changes nothing) — only a human PR editing manifest.yml can change it",
			), nil
		case OwnershipGenerated:
			return notPatchCandidate(
				resolveDocMode(input), &ownership, EnforcementGeneratedExcluded, generatedBlockedReason,
			), nil
		case OwnershipExternal:
			return notPatchCandidate(
				resolveDocMode(input), &ownership, EnforcementExternalExcluded, externalBlockedReason,
			), nil
		}
	}

	mode := resolveDocMode(input)
	hasExplicitMode := input.EntryMode != nil || input.DefaultsMode != nil
	enforcement := ""
	if hasExplicitMode {
		enforcement = EnforcementMode
	}
	if mode == schemas.DocModeLocked {
		return notPatchCandidate(mode, input.Ownership, enforcement, lockedBlockedReason), nil
	}
	return ModeResolution{
		Mode:           mode,
		Ownership:      input.Ownership,
		Writable:       true,
		PatchCandidate: true,
		AutoApply:      mode == schemas.DocModeAuto,
		Enforcement:    enforcement,
	}, nil
}

// IsDocMode is the runtime check exported for consumers that must validate an
// untyped DocMode value (TS isDocMode over `unknown`). Non-strings — including
// nil — are rejected.
func IsDocMode(value any) bool {
	mode, ok := value.(string)
	if !ok {
		return false
	}
	return schemas.IsValidDocMode(mode)
}
