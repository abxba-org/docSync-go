package precedence

import (
	"github.com/DUDE616/docSync-go/internal/modes"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// InvocationFlags is the tighten-only invocation overlay (ARCH §11.3,
// precedence layer 1). Optional fields are pointers: nil means "absent" (TS
// `undefined`).
//
// There is deliberately NO field that can loosen a mode or bypass a lock: the
// TS type carried `allowAutoOverride?: never` to document that no such
// override exists and must never exist without a new, reviewed schema
// version. Keep it that way.
type InvocationFlags struct {
	// ReportOnly forces the report-only posture (no writes anywhere).
	ReportOnly *bool
	// DryRun implies the report-only posture.
	DryRun *bool
	// Strict is a stricter checking posture (Phase 0: recorded for provenance
	// only).
	Strict *bool
}

// PrecedenceInput is one document's precedence resolution input. Optional
// fields are pointers: nil means "absent" (TS `undefined`).
type PrecedenceInput struct {
	// EntryMode is the mode from the per-document manifest entry (may be
	// absent).
	EntryMode *schemas.DocMode
	// EntryTriggers are universal triggers declared on the entry (provenance
	// only in Phase 0).
	EntryTriggers []string
	// EntryIgnore are ignored triggers declared on the entry (provenance only
	// in Phase 0).
	EntryIgnore []string
	// DefaultsMode is the mode from the manifest `defaults:` block (may be
	// absent).
	DefaultsMode *schemas.DocMode
	// Floors are the manifest-provided org floors — always evaluated;
	// violations are errors, never clamps.
	Floors []modes.OrgFloor
	// OrgPolicy is the parsed org policy (P12-T13; OD-R30) — provides floors +
	// learning.mode default.
	OrgPolicy *schemas.OrgPolicy
	// Flags is the tighten-only overlay, applied last.
	Flags *InvocationFlags
	// Ownership is the detected/declared ownership (D2: floors dominate every
	// other layer).
	Ownership *modes.OwnershipCategory
	// DocPath is the document path the configuration is resolved for.
	DocPath string
}

// Layer values for AppliedSource (TS AppliedSource['layer'] union).
const (
	LayerFlags    = "flags"
	LayerEntry    = "entry"
	LayerDefaults = "defaults"
	LayerFloor    = "floor"
	LayerBuiltin  = "builtin"
)

// Field values recorded in AppliedSource (TS field unions).
const (
	FieldMode                 = "mode"
	FieldLearningMode         = "learningMode"
	FieldLearningModeDefault  = "learningModeDefault"
	FieldAbstainOnUncertainty = "abstainOnUncertainty"
	FieldReportOnly           = "reportOnly"
	FieldMaxMode              = "maxMode"
	FieldTriggers             = "triggers"
	FieldIgnore               = "ignore"
	FieldDryRun               = "dryRun"
	FieldStrict               = "strict"
)

// AppliedSource is the provenance of one resolved decision, for reviewability
// (appliedSources).
type AppliedSource struct {
	Layer string
	Field string
	Value any
}

// EffectiveConfig is the resolved effective configuration for one document.
type EffectiveConfig struct {
	// Mode is the effective mutability mode after ownership floors (D2
	// embedded).
	Mode schemas.DocMode
	// PatchCandidate reports whether the document enters the patch candidate
	// pipeline (auto/suggest/human only).
	PatchCandidate bool
	// AutoApply reports whether the document may auto-commit/apply — requires
	// mode "auto" AND report-only off.
	AutoApply bool
	// ReportOnly is the report-only posture. Built-in default is true
	// (check-only, D10/PLAN §0.1); dry-run and explicit ReportOnly=true keep
	// it on; only an explicit flags.reportOnly=false may drop it (reserved for
	// a future reviewed config).
	ReportOnly bool
	// LearningMode is the built-in learning posture for V1 (versioned for the
	// future).
	LearningMode schemas.LearningMode
	// LearningModeDefault is the org default learning mode from org policy
	// (P12-T13; OD-R30).
	LearningModeDefault schemas.LearningMode
	// AbstainOnUncertainty is the built-in invariant: uncertainty routes to
	// REVIEW, never a guess (D6). Always true.
	AbstainOnUncertainty bool
	// AppliedSources is the provenance of every decision, in application order
	// (reviewability).
	AppliedSources []AppliedSource
}

// builtinMode is the built-in default mode (TS BUILTIN_MODE).
const builtinMode = schemas.DocModeSuggest

// orgPolicyFloors converts the org policy floors into the core OrgFloor union
// (P12-T13). The conversion is a plain field copy: `reviewed` and
// `requiredReviewers` survive EXACTLY as the org declared them (dropping
// `reviewed` would make a declared review still nag; inventing it would fail
// closed the wrong way).
func orgPolicyFloors(policy *schemas.OrgPolicy) []modes.OrgFloor {
	if policy == nil {
		return nil
	}
	converted := make([]modes.OrgFloor, 0, len(policy.Floors))
	for _, floor := range policy.Floors {
		switch floor.Kind {
		case schemas.FloorKindModeCeiling:
			converted = append(converted, modes.OrgFloor{
				Kind:    schemas.FloorKindModeCeiling,
				Pattern: floor.Pattern,
				MaxMode: derefDocMode(floor.MaxMode),
			})
		case schemas.FloorKindReportOnlyUntilReviewed:
			converted = append(converted, modes.OrgFloor{
				Kind:              schemas.FloorKindReportOnlyUntilReviewed,
				Pattern:           floor.Pattern,
				RequiredReviewers: floor.RequiredReviewers,
				Reviewed:          floor.Reviewed,
			})
		}
	}
	return converted
}

func derefDocMode(mode *schemas.DocMode) schemas.DocMode {
	if mode == nil {
		return ""
	}
	return *mode
}

// ResolveEffectiveConfig resolves the full effective configuration for one
// document across all precedence layers.
//
// It returns a modes.ModeFloorViolationError when the effective mode exceeds a
// matching org floor (V-P0-6 — never clamped) and a modes.ModeConflictError on
// malformed mode/floor inputs (fail-closed, D8). Pure: same input → deep-equal
// output.
func ResolveEffectiveConfig(input PrecedenceInput) (EffectiveConfig, error) {
	applied := []AppliedSource{
		{Layer: LayerBuiltin, Field: FieldMode, Value: builtinMode},
		{Layer: LayerBuiltin, Field: FieldLearningMode, Value: schemas.LearningModePropose},
		{Layer: LayerBuiltin, Field: FieldLearningModeDefault, Value: schemas.LearningModePropose},
		{Layer: LayerBuiltin, Field: FieldAbstainOnUncertainty, Value: true},
		{Layer: LayerBuiltin, Field: FieldReportOnly, Value: true},
	}

	// Convert org policy floors to the core OrgFloor union (P12-T13) and take
	// the org learning default.
	policyFloors := orgPolicyFloors(input.OrgPolicy)
	learningModeDefault := schemas.LearningModePropose
	if input.OrgPolicy != nil && input.OrgPolicy.Learning != nil && input.OrgPolicy.Learning.DefaultMode != nil {
		learningModeDefault = *input.OrgPolicy.Learning.DefaultMode
		applied = append(applied, AppliedSource{
			Layer: LayerBuiltin, Field: FieldLearningModeDefault, Value: learningModeDefault,
		})
	}

	// Layer 4: manifest defaults block.
	if input.DefaultsMode != nil {
		applied = append(applied, AppliedSource{Layer: LayerDefaults, Field: FieldMode, Value: *input.DefaultsMode})
	}
	// Layer 3: per-document entry.
	if input.EntryMode != nil {
		applied = append(applied, AppliedSource{Layer: LayerEntry, Field: FieldMode, Value: *input.EntryMode})
	}
	if input.EntryTriggers != nil {
		applied = append(applied, AppliedSource{Layer: LayerEntry, Field: FieldTriggers, Value: append([]string(nil), input.EntryTriggers...)})
	}
	if input.EntryIgnore != nil {
		applied = append(applied, AppliedSource{Layer: LayerEntry, Field: FieldIgnore, Value: append([]string(nil), input.EntryIgnore...)})
	}

	// D2 — modes + ownership floors composed; cannot be bypassed downstream.
	resolution, err := modes.ResolveEffectiveMode(modes.ModeResolutionInput{
		EntryMode:    input.EntryMode,
		DefaultsMode: input.DefaultsMode,
		Ownership:    input.Ownership,
	})
	if err != nil {
		return EffectiveConfig{}, err
	}

	// Layer 2: org floors dominate modes — a violation is an error, never a
	// clamp. Compose floors: org policy floors (the floor) + manifest-provided
	// floors. Per ARCH §11.2 precedence: "flags > per-doc > manifest defaults >
	// org floors > built-ins".
	composedFloors := make([]modes.OrgFloor, 0, len(policyFloors)+len(input.Floors))
	composedFloors = append(composedFloors, policyFloors...)
	composedFloors = append(composedFloors, input.Floors...)
	if err := modes.EnforceFloors(input.DocPath, resolution.Mode, composedFloors); err != nil {
		return EffectiveConfig{}, err
	}
	if floorMax, ok, err := modes.ComposedFloorMax(composedFloors, input.DocPath); err != nil {
		return EffectiveConfig{}, err
	} else if ok {
		applied = append(applied, AppliedSource{Layer: LayerFloor, Field: FieldMaxMode, Value: floorMax})
	}

	// Layer 1 (last): invocation flags, tighten-only overlay. reportOnly is ON
	// by default (check-only built-in); dry-run always forces it back ON.
	reportOnly := true
	if input.Flags != nil && input.Flags.ReportOnly != nil {
		reportOnly = *input.Flags.ReportOnly
	}
	if input.Flags != nil && input.Flags.DryRun != nil && *input.Flags.DryRun {
		reportOnly = true
	}
	if input.Flags != nil {
		if input.Flags.ReportOnly != nil {
			applied = append(applied, AppliedSource{Layer: LayerFlags, Field: FieldReportOnly, Value: *input.Flags.ReportOnly})
		}
		if input.Flags.DryRun != nil {
			applied = append(applied, AppliedSource{Layer: LayerFlags, Field: FieldDryRun, Value: *input.Flags.DryRun})
		}
		if input.Flags.Strict != nil {
			applied = append(applied, AppliedSource{Layer: LayerFlags, Field: FieldStrict, Value: *input.Flags.Strict})
		}
	}

	return EffectiveConfig{
		Mode:                 resolution.Mode,
		PatchCandidate:       resolution.PatchCandidate,
		AutoApply:            resolution.AutoApply && !reportOnly,
		ReportOnly:           reportOnly,
		LearningMode:         schemas.LearningModePropose,
		LearningModeDefault:  learningModeDefault,
		AbstainOnUncertainty: true,
		AppliedSources:       applied,
	}, nil
}
