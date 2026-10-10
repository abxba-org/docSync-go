package modes

import (
	"sort"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ModeCeilingFloor is a mode ceiling floor — maximum permissive mode for
// matching documents (TS ModeCeilingFloor).
type ModeCeilingFloor struct {
	Kind    string
	Pattern string
	MaxMode schemas.DocMode
}

// ReportOnlyUntilReviewedFloor is a report-only-until-reviewed floor —
// advisory until the manifest is reviewed (TS ReportOnlyUntilReviewedFloor).
type ReportOnlyUntilReviewedFloor struct {
	Kind              string
	Pattern           string
	RequiredReviewers []string
	// Reviewed mirrors the TS optional field: nil means the org has NOT
	// declared the matching manifest reviewed (absence is not a review —
	// fail closed, P12-T13).
	Reviewed *bool
}

// OrgFloor is one org floor. The TS discriminated union is ported as a single
// tagged struct — the same shape schemas.OrgFloor uses — so a floor converts
// from the org-policy wire format by a plain field copy and marshals exactly
// the keys its variant carries.
//
// Only the fields of the active Kind are meaningful:
//
//   - kind 'mode_ceiling'               → Pattern + MaxMode
//   - kind 'report_only_until_reviewed' → Pattern + RequiredReviewers? + Reviewed?
type OrgFloor struct {
	Kind              string `json:"kind"`
	Pattern           string `json:"pattern"`
	MaxMode           schemas.DocMode
	RequiredReviewers []string
	Reviewed          *bool
}

// IsModeCeilingFloor is the type guard for mode-ceiling floors.
func IsModeCeilingFloor(floor OrgFloor) bool {
	return floor.Kind == schemas.FloorKindModeCeiling
}

// IsReportOnlyFloor is the type guard for report-only-until-reviewed floors.
func IsReportOnlyFloor(floor OrgFloor) bool {
	return floor.Kind == schemas.FloorKindReportOnlyUntilReviewed
}

// FloorViolation is one violated (document, mode-ceiling floor) pair reported
// by CheckFloors.
type FloorViolation struct {
	// Path is the document path whose requested mode was checked.
	Path string
	// RequestedMode is the effective mode the document would resolve to.
	RequestedMode schemas.DocMode
	// Floor is the violated floor.
	Floor OrgFloor
	// Message names path, requested mode, and floor maximum.
	Message string
}

// FloorCheckAdvisory is an advisory for a report-only floor whose manifest has
// not been reviewed.
type FloorCheckAdvisory struct {
	// Path is the document path checked.
	Path string
	// Floor is the report-only floor that applies.
	Floor OrgFloor
	// Message is the human-readable advisory message.
	Message string
}

// FloorCheckResult is the outcome of CheckFloors: Ok reports whether the mode
// is permitted (TS FloorCheckOk/FloorCheckErr), Errors lists every violated
// (doc, floor) pair when it is not, and Advisories carries the non-blocking
// report-only advisories either way.
type FloorCheckResult struct {
	Ok         bool
	Errors     []FloorViolation
	Advisories []FloorCheckAdvisory
}

// FloorCheckParams is the CheckFloors parameter set (TS `{ path, mode, floors }`).
type FloorCheckParams struct {
	Path   string
	Mode   *schemas.DocMode
	Floors []OrgFloor
}

// AssertValidFloorPattern fails closed on malformed floor patterns (D8):
// reject, never ignore. The pattern language is deliberately minimal (the glob
// engine is owned elsewhere) and supports exactly an exact document path, a
// bare directory (≈ `dir/**`), and a trailing `dir/**` glob.
func AssertValidFloorPattern(pattern string) error {
	fail := func(why string) error {
		return &ModeConflictError{
			Message: `malformed floor pattern "` + pattern + `": ` + why,
		}
	}
	if len(pattern) == 0 {
		return fail("must not be empty")
	}
	if strings.HasPrefix(pattern, "/") {
		return fail("must be relative, not absolute")
	}
	if strings.Contains(pattern, "\\") {
		return fail("must use POSIX '/' separators")
	}
	segments := strings.Split(pattern, "/")
	for index, segment := range segments {
		if segment == "**" {
			if index != len(segments)-1 {
				return fail("'**' is only supported as the final segment")
			}
			continue
		}
		if len(segment) == 0 {
			return fail("must not contain empty segments (double slashes or trailing '/')")
		}
		if segment == "." || segment == ".." {
			return fail("must not contain '.' or '..' segments")
		}
		if strings.ContainsAny(segment, "*?{[") {
			return fail("only exact paths, bare directories, and trailing 'dir/**' are supported")
		}
	}
	return nil
}

// assertValidFloorMaxMode fails closed on a floor whose maxMode is not a
// DocMode at all.
func assertValidFloorMaxMode(floor OrgFloor) error {
	if !schemas.IsValidDocMode(string(floor.MaxMode)) {
		return &ModeConflictError{
			Message: "malformed floor: maxMode " + jsQuote(string(floor.MaxMode)) +
				" is not one of " + joinDocModes(schemas.DocModeValues),
		}
	}
	return nil
}

// jsQuote renders a string the way TS JSON.stringify did in the original
// maxMode diagnostic.
func jsQuote(value string) string {
	return `"` + value + `"`
}

func joinDocModes(modes []schemas.DocMode) string {
	names := make([]string, 0, len(modes))
	for _, mode := range modes {
		names = append(names, string(mode))
	}
	return strings.Join(names, ", ")
}

// FloorMatchesPath reports whether a floor pattern applies to a document path.
// Supports exactly: exact path, bare `dir` (≈ `dir/**`), and trailing `dir/**`
// (`**` alone matches everything). When matching could ever be ambiguous the
// pattern over-matches rather than under-matches — floors failing closed is the
// safe direction.
//
// Malformed patterns are rejected with a ModeConflictError instead of silently
// never matching: a floor that fails open would be a security hole (D8).
func FloorMatchesPath(pattern, path string) (bool, error) {
	if err := AssertValidFloorPattern(pattern); err != nil {
		return false, err
	}
	if pattern == path {
		return true, nil
	}
	if pattern == "**" {
		return true, nil
	}
	if strings.HasSuffix(pattern, "/**") {
		dir := strings.TrimSuffix(pattern, "/**")
		if dir == "" {
			return true, nil
		}
		return path == dir || strings.HasPrefix(path, dir+"/"), nil
	}
	return strings.HasPrefix(path, pattern+"/"), nil
}

// FloorSummary lists the floors applicable to docPath, sorted deterministically
// by pattern. Every floor is validated first (malformed floors are rejected
// even when they would not match).
func FloorSummary(floors []OrgFloor, docPath string) ([]OrgFloor, error) {
	applicable := make([]OrgFloor, 0, len(floors))
	for _, floor := range floors {
		if IsModeCeilingFloor(floor) {
			if err := assertValidFloorMaxMode(floor); err != nil {
				return nil, err
			}
		}
		matches, err := FloorMatchesPath(floor.Pattern, docPath)
		if err != nil {
			return nil, err
		}
		if matches {
			applicable = append(applicable, floor)
		}
	}
	sort.SliceStable(applicable, func(i, j int) bool {
		return applicable[i].Pattern < applicable[j].Pattern
	})
	return applicable, nil
}

// ComposedFloorMax returns the composed most-restrictive maximum for docPath
// from mode-ceiling floors only. The bool is false when no mode-ceiling floor
// applies (TS null). Overlapping floors compose via min on ModeLadder ("most
// restrictive wins") — that is composition, not a conflict. Report-only floors
// do not affect the composed maximum.
func ComposedFloorMax(floors []OrgFloor, docPath string) (schemas.DocMode, bool, error) {
	applicable, err := FloorSummary(floors, docPath)
	if err != nil {
		return "", false, err
	}
	var max schemas.DocMode
	found := false
	for _, floor := range applicable {
		if !IsModeCeilingFloor(floor) {
			continue
		}
		if !found || ModeLadder[floor.MaxMode] < ModeLadder[max] {
			max = floor.MaxMode
			found = true
		}
	}
	if !found {
		return "", false, nil
	}
	return max, true, nil
}

// CheckFloors checks one document path + requested effective mode against org
// floors. Mode-ceiling violations are errors (blocking); report-only floors
// produce advisories (non-blocking). Overlapping mode-ceiling floors compose
// to the most restrictive maximum; every violated (doc, floor) pair is
// reported, sorted deterministically. A `maxMode` of `locked`/`generated` is
// legal (it just tightens further).
//
// Malformed floors are rejected with a ModeConflictError even when they do not
// match the path under check.
func CheckFloors(params FloorCheckParams) (FloorCheckResult, error) {
	applicable, err := FloorSummary(params.Floors, params.Path)
	if err != nil {
		return FloorCheckResult{}, err
	}
	result := FloorCheckResult{Ok: true}

	// Collect advisories from report-only floors (always, regardless of mode).
	// The advisory applies ONLY until the org declares the manifest reviewed
	// (P12-T13: "report-only until manifest reviewed"); `reviewed: true` is a
	// human-owned declaration in the org policy file, so the advisory stops
	// exactly when the review act happened — absence is NOT a review (fail
	// closed).
	for _, floor := range applicable {
		if IsReportOnlyFloor(floor) && (floor.Reviewed == nil || !*floor.Reviewed) {
			result.Advisories = append(result.Advisories, FloorCheckAdvisory{
				Path:  params.Path,
				Floor: floor,
				Message: `"` + params.Path + `" matches report-only floor "` + floor.Pattern +
					`" — manifest not yet reviewed`,
			})
		}
	}

	// Check mode-ceiling violations only when a mode is provided.
	if params.Mode != nil {
		mode := *params.Mode
		for _, floor := range applicable {
			if IsModeCeilingFloor(floor) && ModeLadder[mode] > ModeLadder[floor.MaxMode] {
				result.Errors = append(result.Errors, FloorViolation{
					Path:          params.Path,
					RequestedMode: mode,
					Floor:         floor,
					Message: `"` + params.Path + `" requests mode "` + string(mode) +
						`" which exceeds org floor maximum "` + string(floor.MaxMode) +
						`" (pattern "` + floor.Pattern + `")`,
				})
			}
		}
	}

	sortAdvisories(result.Advisories)
	if len(result.Errors) == 0 {
		return result, nil
	}
	result.Ok = false
	sortFloorViolations(result.Errors)
	return result, nil
}

func sortAdvisories(advisories []FloorCheckAdvisory) {
	sort.SliceStable(advisories, func(i, j int) bool {
		return advisories[i].Floor.Pattern < advisories[j].Floor.Pattern
	})
}

// sortFloorViolations orders violations by (floor pattern, floor maxMode) —
// the TS byFloorThenMode comparator.
func sortFloorViolations(violations []FloorViolation) {
	sort.SliceStable(violations, func(i, j int) bool {
		a, b := violations[i], violations[j]
		if a.Floor.Pattern != b.Floor.Pattern {
			return a.Floor.Pattern < b.Floor.Pattern
		}
		return ModeLadder[a.Floor.MaxMode] < ModeLadder[b.Floor.MaxMode]
	})
}

// EnforceFloors is the hard form of CheckFloors: it returns a
// ModeFloorViolationError when the effective mode exceeds a mode-ceiling floor
// — for the MOST restrictive violated floor, deterministically chosen. Never
// clamps (V-P0-6, V-P12-12). Report-only advisories do NOT cause an error —
// they are returned by CheckFloors for the caller to handle.
func EnforceFloors(path string, mode schemas.DocMode, floors []OrgFloor) error {
	result, err := CheckFloors(FloorCheckParams{Path: path, Mode: &mode, Floors: floors})
	if err != nil {
		return err
	}
	if result.Ok {
		return nil
	}
	worst := result.Errors[0]
	for _, candidate := range result.Errors[1:] {
		if ModeLadder[candidate.Floor.MaxMode] < ModeLadder[worst.Floor.MaxMode] ||
			(ModeLadder[candidate.Floor.MaxMode] == ModeLadder[worst.Floor.MaxMode] &&
				candidate.Floor.Pattern < worst.Floor.Pattern) {
			worst = candidate
		}
	}
	return newModeFloorViolationError(ModeFloorViolation{
		Path:          worst.Path,
		RequestedMode: worst.RequestedMode,
		FloorMode:     worst.Floor.MaxMode,
	}, worst.Message)
}
