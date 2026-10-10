package policy

// Ported from packages/policy/src/governance/learning-mode.ts — learning.mode
// governance (P10-T22; ARCH §8.9, §11.2/§11.3, CTX §24):
// off | propose | canary | auto — DEFAULT `propose`. `canary`/`auto` are
// ESCALATIONS OF TRUST, not separate algorithms: they automate phases of the
// ONE promotion pipeline. Bypass-proofing (INV-7/INV-9): enabling canary/auto
// REQUIRES recorded gate evidence (the latest promotion attempt passed every
// gate); the gates themselves re-run inside the pipeline regardless of mode —
// config cannot skip them. `off` disables proposal processing entirely.
// Permission story (V1): repo-owner config wins over org policy defaults
// (precedence: explicit repo config > org default > shipped default).
//
// V3 scope note: `canary`/`auto` are Phase-12 territory (shadow/canary run
// BEFORE production); the resolver here is the governance seam those waves
// consume — it never promotes anything by itself.

import (
	"encoding/json"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// LearningModeValues lists the learning modes in canonical order
// (TS: LEARNING_MODE_VALUES — Object.freeze(['off','propose','canary','auto'])).
var LearningModeValues = []LearningMode{
	LearningModeOff, LearningModePropose, LearningModeCanary, LearningModeAuto,
}

// LearningMode is one of the four learning modes (TS: LearningMode).
type LearningMode string

// The four learning modes in canonical order.
const (
	LearningModeOff     LearningMode = "off"
	LearningModePropose LearningMode = "propose"
	LearningModeCanary  LearningMode = "canary"
	LearningModeAuto    LearningMode = "auto"
)

// LearningModeDefault is the shipped default — `propose`, never `auto`
// (Phase 12 enables auto) (TS: LEARNING_MODE_DEFAULT).
const LearningModeDefault LearningMode = LearningModePropose

// LearningMode source identifiers (TS: LearningModeResolution['source']).
const (
	LearningModeSourceRepoConfig     = "repo-config"
	LearningModeSourceOrgDefault     = "org-default"
	LearningModeSourceShippedDefault = "shipped-default"
)

// LearningModeValidationError ports the TS LearningModeValidationError class:
// every fail-closed learning-config refusal carries it (config cannot bypass
// the gates). Go returns values, so the class identity is preserved as a
// coded error type callers match with errors.As.
type LearningModeValidationError struct {
	coded *docutil.Error
}

// Error renders the coded message (EDOCSYNC_VALIDATION: …).
func (e *LearningModeValidationError) Error() string { return e.coded.Error() }

// Unwrap exposes the coded error so docutil.CodeOf reports EDOCSYNC_VALIDATION.
func (e *LearningModeValidationError) Unwrap() error { return e.coded }

func newLearningModeValidationError(message string) *LearningModeValidationError {
	return &LearningModeValidationError{coded: docutil.New(docutil.CodeValidation, message)}
}

// IsValidLearningMode reports membership in LEARNING_MODE_VALUES.
func IsValidLearningMode(mode string) bool {
	for _, candidate := range LearningModeValues {
		if string(candidate) == mode {
			return true
		}
	}
	return false
}

// IsLearningModeEscalation reports whether mode is an escalation of trust
// (canary/auto) — the modes that require recorded gate evidence.
func IsLearningModeEscalation(mode LearningMode) bool {
	return mode == LearningModeCanary || mode == LearningModeAuto
}

// LearningModeResolutionInput is the resolver input
// (TS: LearningModeResolutionInput).
type LearningModeResolutionInput struct {
	// RepoConfig is the repo-level config (`.docsync` config home — the single
	// config home). Untrusted: strictly validated like the zod schema.
	RepoConfig any
	// OrgDefault is the org-policy default (may be absent — nil).
	OrgDefault *LearningMode
	// LatestPromotionGatesPassed is the gate evidence: the latest promotion
	// attempt passed every gate (OD-R24 gate library). Tri-state — only a
	// recorded `true` satisfies the escalation requirement (nil == absent).
	LatestPromotionGatesPassed *bool
}

// gatesPassed reports the recorded gate evidence: ONLY a recorded true counts.
func (i LearningModeResolutionInput) gatesPassed() bool {
	return i.LatestPromotionGatesPassed != nil && *i.LatestPromotionGatesPassed
}

// LearningModeResolution is the resolver outcome
// (TS: LearningModeResolution).
type LearningModeResolution struct {
	Mode        LearningMode
	Source      string
	Escalations []string
}

// parseLearningModeConfig strictly validates the repo config against
// LEARNING_MODE_CONFIG_SCHEMA (z.strictObject semantics: unknown keys are
// rejected, `learning` and `learning.mode` are optional). It returns the
// repo-level mode (nil when unset) or the zod-shaped issues.
func parseLearningModeConfig(repoConfig any) (*LearningMode, []string) {
	var issues []string
	root, ok := repoConfig.(map[string]any)
	if !ok {
		return nil, []string{"Required"}
	}
	for key := range root {
		if key != "learning" {
			issues = append(issues, "Unrecognized key: \""+key+"\"")
		}
	}
	learning, present := root["learning"]
	if !present {
		if len(issues) > 0 {
			return nil, issues
		}
		return nil, nil
	}
	if learning == nil {
		issues = append(issues, "learning: Expected object, received null")
		return nil, issues
	}
	learningObject, ok := learning.(map[string]any)
	if !ok {
		issues = append(issues, "learning: Expected object, received "+jsonTypeName(learning))
		return nil, issues
	}
	for key := range learningObject {
		if key != "mode" {
			issues = append(issues, "learning: Unrecognized key: \""+key+"\"")
		}
	}
	rawMode, modePresent := learningObject["mode"]
	if !modePresent {
		if len(issues) > 0 {
			return nil, issues
		}
		return nil, nil
	}
	modeText, ok := rawMode.(string)
	if !ok {
		issues = append(issues, "learning.mode: Invalid enum value. Expected 'off' | 'propose' | "+
			"'canary' | 'auto', received "+jsonTypeName(rawMode))
		return nil, issues
	}
	mode := LearningMode(modeText)
	if !IsValidLearningMode(modeText) {
		issues = append(issues, "learning.mode: Invalid enum value. Expected 'off' | 'propose' | "+
			"'canary' | 'auto', received '"+modeText+"'")
		return nil, issues
	}
	if len(issues) > 0 {
		return nil, issues
	}
	return &mode, nil
}

// jsonTypeName renders the TS-flavored type name used in zod issue messages.
func jsonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64, int, int64, json.Number:
		return "number"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// ResolveLearningMode ports resolveLearningMode: repo config wins over the org
// default, which wins over the shipped default; canary/auto escalate ONLY with
// recorded gate evidence (fail closed — a validation error otherwise).
func ResolveLearningMode(input LearningModeResolutionInput) (LearningModeResolution, error) {
	repoMode, issues := parseLearningModeConfig(input.RepoConfig)
	if len(issues) > 0 {
		return LearningModeResolution{}, newLearningModeValidationError(
			"learning config invalid: " + strings.Join(issues, "; "))
	}
	escalations := []string{}
	if repoMode != nil {
		if IsLearningModeEscalation(*repoMode) {
			if !input.gatesPassed() {
				return LearningModeResolution{}, newLearningModeValidationError(
					"learning.mode=" + string(*repoMode) + " requires recorded gate evidence (the latest " +
						"promotion attempt passed every gate) — enabling without it is a validation error " +
						"(ARCH §8.9; INV-7/INV-9)")
			}
			escalations = append(escalations, string(*repoMode)+
				": gates re-run inside the pipeline on every promotion — config cannot bypass them")
		}
		return LearningModeResolution{
			Mode:        *repoMode,
			Source:      LearningModeSourceRepoConfig,
			Escalations: escalations,
		}, nil
	}
	if input.OrgDefault != nil {
		orgDefault := *input.OrgDefault
		if IsLearningModeEscalation(orgDefault) {
			// an org default may escalate ONLY with the same gate evidence
			if !input.gatesPassed() {
				return LearningModeResolution{}, newLearningModeValidationError(
					"org learning.mode=" + string(orgDefault) +
						" requires recorded gate evidence — enabling without it is a validation error")
			}
			escalations = append(escalations, "org-default "+string(orgDefault)+
				": gates re-run inside the pipeline")
		}
		return LearningModeResolution{
			Mode:        orgDefault,
			Source:      LearningModeSourceOrgDefault,
			Escalations: escalations,
		}, nil
	}
	return LearningModeResolution{
		Mode:        LearningModeDefault,
		Source:      LearningModeSourceShippedDefault,
		Escalations: escalations,
	}, nil
}
