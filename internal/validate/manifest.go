package validate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// MANIFEST_REL_PATH is the repo-relative manifest location (D1).
const MANIFEST_REL_PATH = ".docsync/manifest.yml"

// ORG_POLICY_REL_PATH is the org policy file location at repo root (OD-R30).
const ORG_POLICY_REL_PATH = ".docsync/org-policy.yml"

// orgPolicySchemaID mirrors schemas.OrgPolicySchemaID (kept unexported
// there) for the fail-closed "violates <id>" diagnostic text.
const orgPolicySchemaID = "docsync.org-policy/v1"

// ManifestValidateOptions configures LoadAndValidateManifest.
type ManifestValidateOptions struct {
	// Floors are org floors checked per entry (violations are hard errors,
	// V-P0-6, V-P12-12). Composed ON TOP of any floors from
	// .docsync/org-policy.yml.
	Floors []schemas.OrgFloor
	// LoadOrgPolicy controls loading `.docsync/org-policy.yml` (default:
	// true). Absence of the file is legitimately "no org floors"; a PRESENT
	// but invalid file is a hard error.
	LoadOrgPolicy *bool
}

// ManifestValidationResult is the outcome of the manifest stage. Never
// throws for configuration problems (D8): every failure becomes a
// diagnostic so the caller can present the FULL error list.
type ManifestValidationResult struct {
	// Manifest is the parsed+schema-valid manifest, or nil when validation
	// failed.
	Manifest *schemas.Manifest
	// Diagnostics holds all hard errors and warnings, sorted deterministically.
	Diagnostics []Diagnostic
	// Advisories are report-only advisories from org policy floors
	// (non-blocking, V-P12-12).
	Advisories []FloorCheckAdvisory
	// OrgPolicy is the parsed org policy, if loaded.
	OrgPolicy *schemas.OrgPolicy
}

// FloorCheckAdvisory is a report-only floor whose manifest has not been
// reviewed (ported from modes/floors.ts).
type FloorCheckAdvisory struct {
	// Path is the document path checked.
	Path string `json:"path"`
	// Floor is the report-only floor that applies.
	Floor schemas.OrgFloor `json:"floor"`
	// Message is the human-readable advisory message.
	Message string `json:"message"`
}

const positionUnavailableHint = "position unavailable for this issue (manifest-level fallback)"

// position is a 1-based source position; line 0 means "no known position"
// (file-level). fallback marks the file-level default (line 1) used when no
// node could be located for an issue path.
type position struct {
	line     int
	column   int
	fallback bool
}

// LoadAndValidateManifest loads and validates `.docsync/manifest.yml` (D8).
// Never throws for configuration problems — every failure becomes a
// diagnostic so the caller can present the FULL error list (never the first
// error). The only error return is a genuine runtime I/O failure (an
// unreadable org policy file, which the TS loader rethrows rather than
// silently dropping the org's floors — INV-9/D8).
func LoadAndValidateManifest(repoRoot string, options ManifestValidateOptions) (ManifestValidationResult, error) {
	var diagnostics []Diagnostic
	file := filepath.Join(repoRoot, MANIFEST_REL_PATH)

	source, err := os.ReadFile(file)
	if err != nil {
		if isNotExistOrNotDir(err) {
			diagnostics = append(diagnostics, makeDiagnostic(
				"E_MANIFEST_MISSING",
				"manifest.yml not found — no manifest, no run (D7/D8)",
				position{},
				"create .docsync/manifest.yml with version + documents",
			))
		} else {
			diagnostics = append(diagnostics, makeDiagnostic(
				"E_MANIFEST_IO",
				"failed to read manifest.yml: "+err.Error(),
				position{},
				"",
			))
		}
		sortManifestDiagnostics(diagnostics)
		return ManifestValidationResult{Diagnostics: diagnostics}, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		hint := ""
		line, ok := lineFromYamlError(err)
		if !ok {
			line = 1
			hint = positionUnavailableHint
		}
		diagnostics = append(diagnostics, makeDiagnostic(
			"E_MANIFEST_YAML",
			"invalid YAML: "+firstSemicolonPart(err.Error()),
			position{line: line},
			hint,
		))
		sortManifestDiagnostics(diagnostics)
		return ManifestValidationResult{Diagnostics: diagnostics}, nil
	}

	// Strict-schema unknown keys (OD-R01). The YAML decoder drops unknown
	// keys, so walk the AST explicitly — the same place zod's strictObject
	// reports them — before the typed schema validation below.
	diagnostics = append(diagnostics, unknownKeyDiagnostics(&doc)...)

	// Schema validation with line-precise issue mapping.
	manifest, parseErr := schemas.ParseManifest(source)
	if parseErr != nil {
		var validationErr *schemas.ValidationError
		if asValidationError(parseErr, &validationErr) {
			for _, issue := range validationErr.Issues {
				diagnostics = append(diagnostics, diagnosticForIssue(issue, &doc))
			}
		} else {
			diagnostics = append(diagnostics, makeDiagnostic(
				"E_MANIFEST_SCHEMA",
				"(manifest): "+parseErr.Error(),
				position{line: 1},
				positionUnavailableHint,
			))
		}
	}
	if len(diagnostics) > 0 {
		sortManifestDiagnostics(diagnostics)
		return ManifestValidationResult{Diagnostics: diagnostics}, nil
	}

	// --- Semantic checks (manifest is schema-valid from here on). ---

	// Version support (positive int already guaranteed by the schema).
	if !schemas.IsSupportedManifestVersion(manifest.Version) {
		diagnostics = append(diagnostics, makeDiagnostic(
			"E_MANIFEST_UNSUPPORTED_VERSION",
			fmt.Sprintf("manifest version %d is not supported — supported versions: %s",
				manifest.Version, joinInts(schemas.SupportedManifestVersions)),
			positionForPath(&doc, []string{"version"}),
			"bump support deliberately: a new manifest version requires a reviewed schema + registry entry",
		))
	}

	// Duplicate document paths — reported at the SECOND (and later) occurrence.
	byPath := map[string][]int{}
	for i, entry := range manifest.Documents {
		byPath[entry.Path] = append(byPath[entry.Path], i)
	}
	for _, docPath := range sortedStringKeys(byPath) {
		indices := byPath[docPath]
		for occurrence := 1; occurrence < len(indices); occurrence++ {
			entryIndex := indices[occurrence]
			diagnostics = append(diagnostics, makeDiagnostic(
				"E_MANIFEST_DUPLICATE_PATH",
				fmt.Sprintf("duplicate document path %q (occurrence #%d) — each document path may appear at most once",
					docPath, occurrence+1),
				positionForPath(&doc, []string{"documents", strconv.Itoa(entryIndex), "path"}),
				"",
			))
		}
	}

	// --- Load org policy from repo root (OD-R30; V-P12-12). ---
	var orgPolicy *schemas.OrgPolicy
	var orgPolicyFloors []schemas.OrgFloor
	var orgPolicyAdvisories []FloorCheckAdvisory
	if options.LoadOrgPolicy == nil || *options.LoadOrgPolicy {
		orgPolicyFile := filepath.Join(repoRoot, ORG_POLICY_REL_PATH)
		orgPolicySource, readErr := os.ReadFile(orgPolicyFile)
		if readErr != nil && !isNotExist(readErr) {
			// ABSENCE is legitimately "no org policy" (no floors apply).
			// EVERY other read failure is a fail-closed refusal (INV-9/D8):
			// an unreadable policy file must never be treated as "no
			// floors" — that would silently drop the organization's floors.
			return ManifestValidationResult{}, fmt.Errorf("failed to read org policy %s: %w", ORG_POLICY_REL_PATH, readErr)
		}
		if readErr == nil {
			// Present-but-broken is a HARD VALIDATION ERROR (exit 2), never
			// a silent drop: the file is a human-owned, PR-reviewable
			// artifact, so a malformed one is a config defect the operator
			// must fix. Fail closed.
			parsed := parseOrgPolicy(orgPolicySource)
			if parsed.syntaxError != "" {
				diagnostics = append(diagnostics, makeDiagnostic(
					"E_MANIFEST_ORG_POLICY_INVALID",
					fmt.Sprintf("%s is not valid YAML — refusing to run without the org policy (fail closed, INV-9/D8): %s",
						ORG_POLICY_REL_PATH, parsed.syntaxError),
					position{},
					"",
				))
				sortManifestDiagnostics(diagnostics)
				return ManifestValidationResult{Diagnostics: diagnostics}, nil
			}
			if parsed.policy == nil {
				diagnostics = append(diagnostics, makeDiagnostic(
					"E_MANIFEST_ORG_POLICY_INVALID",
					fmt.Sprintf("%s violates %s — refusing to run without the org policy (fail closed, INV-9/D8): %s",
						ORG_POLICY_REL_PATH, orgPolicySchemaID, formatOrgPolicyIssues(parsed.issues)),
					position{},
					"",
				))
				sortManifestDiagnostics(diagnostics)
				return ManifestValidationResult{Diagnostics: diagnostics}, nil
			}

			orgPolicy = parsed.policy
			orgPolicyFloors = append(orgPolicyFloors, parsed.policy.Floors...)
			// Run report-only floor checks against the manifest to produce
			// advisories. The floor policy schema is deliberately permissive
			// (`pattern` is any non-empty string), so a SCHEMA-LEGAL but
			// malformed pattern reaches the floor engine, which fails closed
			// by erroring instead of silently never matching. That error
			// becomes a diagnostic here in the same guard style as the
			// floor-check block below.
			advisories, floorErr := reportOnlyAdvisories(manifest, orgPolicyFloors)
			if floorErr != nil {
				diagnostics = append(diagnostics, makeDiagnostic(
					"E_MANIFEST_FLOOR_CONFLICT",
					fmt.Sprintf("invalid org floor configuration in %s: %s", ORG_POLICY_REL_PATH, floorErr.Error()),
					position{},
					"org floors are hard errors — the manifest is invalid until fixed; docSync never clamps (V-P0-6)",
				))
				sortManifestDiagnostics(diagnostics)
				return ManifestValidationResult{Diagnostics: diagnostics}, nil
			}
			orgPolicyAdvisories = advisories
		}
	}

	// Org floors: violations are hard errors, never clamps (V-P0-6).
	// Compose floors: org policy floors (the floor) + manifest-provided floors.
	composedFloors := make([]schemas.OrgFloor, 0, len(orgPolicyFloors)+len(options.Floors))
	composedFloors = append(composedFloors, orgPolicyFloors...)
	composedFloors = append(composedFloors, options.Floors...)
	floorViolations, floorErr := checkFloorsForManifest(manifest, composedFloors)
	if floorErr != nil {
		diagnostics = append(diagnostics, makeDiagnostic(
			"E_MANIFEST_FLOOR_CONFLICT",
			"invalid org floor configuration: "+floorErr.Error(),
			position{},
			"",
		))
	} else {
		for _, violation := range floorViolations {
			modePosition := positionForPath(&doc, []string{"documents", strconv.Itoa(violation.entryIndex), "mode"})
			pos := modePosition
			if modePosition.fallback {
				pos = positionForPath(&doc, []string{"documents", strconv.Itoa(violation.entryIndex)})
			}
			diagnostics = append(diagnostics, makeDiagnostic(
				"E_MANIFEST_FLOOR_CONFLICT",
				violation.message,
				pos,
				"org floors are hard errors — the manifest is invalid until fixed; docSync never clamps (V-P0-6)",
			))
		}
	}

	sortManifestDiagnostics(diagnostics)
	hasHardErrors := false
	for _, d := range diagnostics {
		if d.Severity == SeverityError {
			hasHardErrors = true
			break
		}
	}
	resultManifest := manifest
	if hasHardErrors {
		resultManifest = nil
	}
	return ManifestValidationResult{
		Manifest:    resultManifest,
		Diagnostics: diagnostics,
		Advisories:  orgPolicyAdvisories,
		OrgPolicy:   orgPolicy,
	}, nil
}

// ------------------------------------------------------- mode resolution

// ResolveEntryMode resolves the effective mode of one manifest entry.
// Precedence (ARCH §11.3): per-document entry mode → manifest `defaults:`
// block → built-in "suggest". Org floors dominate modes but NEVER clamp:
// a violation is reported as a hard error by the caller (V-P0-6).
//
// TODO(validate): when internal/modes lands, delegate the resolution +
// floor composition to it (modes.resolveEffectiveMode / modes.enforceFloors)
// and keep this as the thin adapter; the semantics above are the authority
// until then.
func ResolveEntryMode(entry schemas.ManifestEntry, defaults *schemas.ManifestDefaults) schemas.DocMode {
	if entry.Mode != nil {
		return *entry.Mode
	}
	if defaults != nil && defaults.Mode != nil {
		return *defaults.Mode
	}
	return schemas.DocModeSuggest
}

// IsPatchCandidateMode reports whether a resolved mode enters the patch
// candidate pipeline: auto/suggest/human only (ARCH §11.2). `locked` is
// terminal and `generated` is excluded outright.
func IsPatchCandidateMode(mode schemas.DocMode) bool {
	switch mode {
	case schemas.DocModeAuto, schemas.DocModeSuggest, schemas.DocModeHuman:
		return true
	default:
		return false
	}
}

// ------------------------------------------------------- manifest floors

// floorViolation is one (entry, floor) violation located at its manifest
// entry index.
type floorViolation struct {
	entryIndex int
	message    string
}

func checkFloorsForManifest(manifest *schemas.Manifest, floors []schemas.OrgFloor) ([]floorViolation, error) {
	var violations []floorViolation
	for i, entry := range manifest.Documents {
		mode := ResolveEntryMode(entry, manifest.Defaults)
		result, err := CheckFloors(entry.Path, mode, floors)
		if err != nil {
			return nil, err
		}
		for _, v := range result.Violations {
			violations = append(violations, floorViolation{entryIndex: i, message: v.Message})
		}
	}
	return violations, nil
}

// reportOnlyAdvisories runs the report-only floor checks against the
// manifest and collects every advisory (non-blocking, V-P12-12).
func reportOnlyAdvisories(manifest *schemas.Manifest, floors []schemas.OrgFloor) ([]FloorCheckAdvisory, error) {
	var advisories []FloorCheckAdvisory
	for _, entry := range manifest.Documents {
		mode := ResolveEntryMode(entry, manifest.Defaults)
		result, err := CheckFloors(entry.Path, mode, floors)
		if err != nil {
			return nil, err
		}
		advisories = append(advisories, result.Advisories...)
	}
	return advisories, nil
}

// FloorConflictError mirrors modes.ModeConflictError: fail closed on
// malformed floor inputs (D8) — reject, never ignore.
type FloorConflictError struct{ msg string }

func (e *FloorConflictError) Error() string { return e.msg }

// FloorViolation is one violated (document, mode-ceiling floor) pair.
type FloorViolation struct {
	// Path is the document path whose requested mode was checked.
	Path string `json:"path"`
	// RequestedMode is the effective mode the document would resolve to.
	RequestedMode schemas.DocMode `json:"requestedMode"`
	// Floor is the violated floor.
	Floor schemas.OrgFloor `json:"floor"`
	// Message names path, requested mode, and floor maximum.
	Message string `json:"message"`
}

// FloorCheckResult holds the floor check outcome: mode-ceiling violations
// (blocking) and report-only advisories (non-blocking).
type FloorCheckResult struct {
	Violations []FloorViolation     `json:"violations"`
	Advisories []FloorCheckAdvisory `json:"advisories"`
}

// modeLadder is the permissiveness ladder over the five ARCH §11.2 modes;
// higher = more permissive. `human` ranks equal to `suggest`; `locked` and
// `generated` rank 0 because they are non-writable CLASSES excluded from the
// patch pipeline outright (ported from modes.MODE_LADDER).
var modeLadder = map[schemas.DocMode]int{
	schemas.DocModeAuto:      3,
	schemas.DocModeSuggest:   2,
	schemas.DocModeHuman:     2,
	schemas.DocModeLocked:    0,
	schemas.DocModeGenerated: 0,
}

// assertValidFloorPattern fails closed on malformed floor patterns (D8):
// reject, never ignore. The pattern language is deliberately minimal —
// exact document path, bare directory (≈ `dir/**`), or trailing-`**`
// directory glob (`**` alone matches everything).
func assertValidFloorPattern(pattern string) error {
	fail := func(why string) error {
		return &FloorConflictError{msg: fmt.Sprintf("malformed floor pattern %q: %s", pattern, why)}
	}
	if pattern == "" {
		return fail("must not be empty")
	}
	if strings.HasPrefix(pattern, "/") {
		return fail("must be relative, not absolute")
	}
	if strings.Contains(pattern, "\\") {
		return fail("must use POSIX '/' separators")
	}
	segments := strings.Split(pattern, "/")
	for i, segment := range segments {
		if segment == "**" {
			if i != len(segments)-1 {
				return fail("'**' is only supported as the final segment")
			}
			continue
		}
		if segment == "" {
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
func assertValidFloorMaxMode(floor schemas.OrgFloor) error {
	if floor.MaxMode == nil {
		return &FloorConflictError{msg: fmt.Sprintf("malformed floor: maxMode undefined is not one of %s",
			strings.Join(modeStrings(schemas.DocModeValues), ", "))}
	}
	if !schemas.IsValidDocMode(string(*floor.MaxMode)) {
		return &FloorConflictError{msg: fmt.Sprintf("malformed floor: maxMode %q is not one of %s",
			*floor.MaxMode, strings.Join(modeStrings(schemas.DocModeValues), ", "))}
	}
	return nil
}

// floorMatchesPath reports whether a floor pattern applies to a document
// path. Supports exactly: exact path, bare `dir` (≈ `dir/**`), and trailing
// `dir/**` (`**` alone matches everything). When matching could ever be
// ambiguous the pattern over-matches rather than under-matches — floors
// failing closed is the safe direction.
func floorMatchesPath(pattern, path string) (bool, error) {
	if err := assertValidFloorPattern(pattern); err != nil {
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

// floorSummary returns the floors applicable to docPath, sorted
// deterministically by pattern. Every floor is validated first (malformed
// floors error even when they would not match).
func floorSummary(floors []schemas.OrgFloor, docPath string) ([]schemas.OrgFloor, error) {
	var applicable []schemas.OrgFloor
	for _, floor := range floors {
		if floor.Kind == schemas.FloorKindModeCeiling {
			if err := assertValidFloorMaxMode(floor); err != nil {
				return nil, err
			}
		}
		matches, err := floorMatchesPath(floor.Pattern, docPath)
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

// CheckFloors checks one document path + effective mode against org floors.
// Mode-ceiling violations are errors (blocking, never a clamp — V-P0-6,
// V-P12-12); report-only floors produce advisories (non-blocking). Overlapping
// mode-ceiling floors compose to the most restrictive maximum; every
// violated (doc, floor) pair is reported, sorted deterministically. A
// malformed floor is a fail-closed error (D8).
//
// TODO(validate): delegate to internal/modes once that package lands; the
// semantics here are the authoritative port of modes/floors.ts until then.
func CheckFloors(path string, mode schemas.DocMode, floors []schemas.OrgFloor) (FloorCheckResult, error) {
	applicable, err := floorSummary(floors, path)
	if err != nil {
		return FloorCheckResult{}, err
	}
	var advisories []FloorCheckAdvisory
	var violations []FloorViolation

	// Collect advisories from report-only floors (always, regardless of
	// mode). The advisory applies ONLY until the org declares the manifest
	// reviewed; `reviewed: true` is a human-owned declaration, so absence
	// is NOT a review (fail closed).
	for _, floor := range applicable {
		if floor.Kind == schemas.FloorKindReportOnlyUntilReviewed && !floorReviewed(floor) {
			advisories = append(advisories, FloorCheckAdvisory{
				Path:    path,
				Floor:   floor,
				Message: fmt.Sprintf("%q matches report-only floor %q — manifest not yet reviewed", path, floor.Pattern),
			})
		}
	}

	// Check mode-ceiling violations (the effective mode is always resolved
	// by the caller; "" skips the check, as an absent TS mode did).
	if mode != "" {
		for _, floor := range applicable {
			if floor.Kind != schemas.FloorKindModeCeiling || floor.MaxMode == nil {
				continue
			}
			if modeLadder[mode] > modeLadder[*floor.MaxMode] {
				violations = append(violations, FloorViolation{
					Path:          path,
					RequestedMode: mode,
					Floor:         floor,
					Message: fmt.Sprintf("%q requests mode %q which exceeds org floor maximum %q (pattern %q)",
						path, mode, *floor.MaxMode, floor.Pattern),
				})
			}
		}
	}

	sort.SliceStable(advisories, func(i, j int) bool {
		return advisories[i].Floor.Pattern < advisories[j].Floor.Pattern
	})
	sort.SliceStable(violations, func(i, j int) bool {
		if violations[i].Floor.Pattern != violations[j].Floor.Pattern {
			return violations[i].Floor.Pattern < violations[j].Floor.Pattern
		}
		return floorMaxLadder(violations[i]) < floorMaxLadder(violations[j])
	})
	return FloorCheckResult{Violations: violations, Advisories: advisories}, nil
}

// floorMaxLadder is the ladder rank of a violation's floor maxMode (0 when
// absent — matching the TS MODE_LADDER[undefined] → undefined comparison
// shape, where a missing maxMode never outranks a real one).
func floorMaxLadder(violation FloorViolation) int {
	if violation.Floor.MaxMode == nil {
		return -1
	}
	return modeLadder[*violation.Floor.MaxMode]
}

// floorReviewed reports whether the org declared the matching manifest
// reviewed. Absence behaves as false — an undeclared review is not a review.
func floorReviewed(floor schemas.OrgFloor) bool {
	return floor.Reviewed != nil && *floor.Reviewed
}

// ------------------------------------------------------- diagnostics

func makeDiagnostic(code, message string, pos position, hint string) Diagnostic {
	d := Diagnostic{
		Severity: SeverityError,
		Code:     code,
		File:     MANIFEST_REL_PATH,
		Message:  message,
	}
	if pos.line != 0 {
		d.Line = pos.line
	}
	if pos.column != 0 {
		d.Column = pos.column
	}
	if hint != "" {
		d.Hint = hint
	}
	return d
}

// positionForPath walks the yaml AST to the node at path and returns its
// 1-based position. Issue paths are property-name based and may point at
// keys that do not exist (e.g. a missing required field), so the walk falls
// back through progressively shorter prefixes — the nearest enclosing node
// is reported — and finally to the file-level position (line 1) with a
// "position unavailable" note.
func positionForPath(doc *yaml.Node, path []string) position {
	for end := len(path); end >= 0; end-- {
		node := nodeAtPath(rootOf(doc), path[:end])
		if node != nil && node.Line > 0 {
			return position{line: node.Line, column: node.Column}
		}
	}
	return position{line: 1, fallback: true}
}

// rootOf unwraps the yaml document node to its content root.
func rootOf(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// nodeAtPath walks mapping keys and sequence indices, returning nil when
// the path does not exist.
func nodeAtPath(node *yaml.Node, path []string) *yaml.Node {
	current := node
	for _, segment := range path {
		if current == nil {
			return nil
		}
		switch current.Kind {
		case yaml.MappingNode:
			current = mappingValue(current, segment)
		case yaml.SequenceNode:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(current.Content) {
				return nil
			}
			current = current.Content[index]
		default:
			return nil
		}
	}
	return current
}

// mappingValue returns the value node for key in a mapping node.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode.Kind == yaml.ScalarNode && keyNode.Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// mappingHasKey reports whether a mapping node carries key.
func mappingHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Kind == yaml.ScalarNode && node.Content[i].Value == key {
			return true
		}
	}
	return false
}

// unknownKeyDiagnostics walks the AST for strict-schema unknown keys
// (OD-R01): root keys are exactly version/documents/defaults; entry keys
// exactly path/mode/sources/triggers/ignore; defaults keys exactly mode.
func unknownKeyDiagnostics(doc *yaml.Node) []Diagnostic {
	var diagnostics []Diagnostic
	root := rootOf(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	const hint = "strict manifest schema rejects unknown keys (OD-R01) — remove or rename them"
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode, valueNode := root.Content[i], root.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			continue
		}
		switch keyNode.Value {
		case "version", "documents", "defaults":
		default:
			diagnostics = append(diagnostics, unknownKeyDiagnostic(keyNode, hint))
			continue
		}
		switch keyNode.Value {
		case "documents":
			if valueNode.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range valueNode.Content {
				if item.Kind != yaml.MappingNode {
					continue
				}
				for j := 0; j+1 < len(item.Content); j += 2 {
					diagnostics = append(diagnostics, entryKeyDiagnostics(item.Content[j], hint)...)
				}
			}
		case "defaults":
			if valueNode.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(valueNode.Content); j += 2 {
				defaultsKey := valueNode.Content[j]
				if defaultsKey.Kind == yaml.ScalarNode && defaultsKey.Value != "mode" {
					diagnostics = append(diagnostics, unknownKeyDiagnostic(defaultsKey, hint))
				}
			}
		}
	}
	return diagnostics
}

func entryKeyDiagnostics(keyNode *yaml.Node, hint string) []Diagnostic {
	if keyNode.Kind != yaml.ScalarNode {
		return nil
	}
	switch keyNode.Value {
	case "path", "mode", "sources", "triggers", "ignore":
		return nil
	default:
		return []Diagnostic{unknownKeyDiagnostic(keyNode, hint)}
	}
}

func unknownKeyDiagnostic(keyNode *yaml.Node, hint string) Diagnostic {
	return makeDiagnostic(
		"E_MANIFEST_SCHEMA",
		fmt.Sprintf("unknown key %q", keyNode.Value),
		position{line: keyNode.Line, column: keyNode.Column},
		hint,
	)
}

// diagnosticForIssue maps one schema issue onto a diagnostic. Known shapes
// are enriched with their specific taxonomy code and hint (mode
// vocabulary, required document path); everything else is E_MANIFEST_SCHEMA.
func diagnosticForIssue(issue schemas.Issue, doc *yaml.Node) Diagnostic {
	path := issue.Path
	pos := positionForPath(doc, path)

	last := ""
	if len(path) > 0 {
		last = path[len(path)-1]
	}

	if last == "mode" {
		if received, ok := receivedEnumValue(issue.Message); ok {
			return makeDiagnostic(
				"E_MANIFEST_UNKNOWN_MODE",
				fmt.Sprintf("unknown mode %q", received),
				pos,
				"expected one of "+strings.Join(modeStrings(schemas.DocModeValues), "|"),
			)
		}
	}

	if last == "path" {
		const hint = "a document path must be a nonempty relative POSIX path without '..' segments or backslashes"
		entryNode := entryNodeForPath(doc, path)
		if entryNode == nil || !mappingHasKey(entryNode, "path") {
			return makeDiagnostic(
				"E_MANIFEST_MISSING_PATH",
				"document path is required on every manifest entry (documents[].path)",
				pos,
				hint,
			)
		}
		valueNode := mappingValue(entryNode, "path")
		if valueNode == nil || valueNode.Value == "" {
			return makeDiagnostic(
				"E_MANIFEST_MISSING_PATH",
				issue.Message,
				pos,
				hint,
			)
		}
		return makeDiagnostic("E_MANIFEST_SCHEMA", pathLabel(path)+": "+issue.Message, pos, hint)
	}

	return makeDiagnostic("E_MANIFEST_SCHEMA", pathLabel(path)+": "+issue.Message, pos, "")
}

// entryNodeForPath returns the entry mapping node for an issue path shaped
// ["documents", "<index>", ...].
func entryNodeForPath(doc *yaml.Node, path []string) *yaml.Node {
	if len(path) < 2 || path[0] != "documents" {
		return nil
	}
	return nodeAtPath(rootOf(doc), path[:2])
}

// pathLabel renders the issue path for generic schema diagnostics.
func pathLabel(path []string) string {
	if len(path) == 0 {
		return "(manifest)"
	}
	return strings.Join(path, ".")
}

// receivedEnumValue extracts the received value from a zod-style enum
// message (`Invalid enum value. Expected 'a' | 'b', received 'x'`).
func receivedEnumValue(message string) (string, bool) {
	const marker = ", received '"
	idx := strings.Index(message, marker)
	if idx < 0 {
		return "", false
	}
	rest := message[idx+len(marker):]
	if end := strings.LastIndex(rest, "'"); end >= 0 {
		return rest[:end], true
	}
	return "", false
}

// ------------------------------------------------------- org policy

// orgPolicyParse is the outcome of parsing `.docsync/org-policy.yml`:
// either a syntax error, schema issues, or the parsed policy.
type orgPolicyParse struct {
	policy      *schemas.OrgPolicy
	syntaxError string
	issues      []schemas.Issue
}

// parseOrgPolicy decodes `.docsync/org-policy.yml`. YAML is normalised to
// JSON first so the schema struct tags (camelCase wire keys) map directly,
// and unknown root keys are rejected explicitly to mirror the TS
// strictObject. Never panics; every failure is reported in the result.
func parseOrgPolicy(source []byte) orgPolicyParse {
	var raw any
	if err := yaml.Unmarshal(source, &raw); err != nil {
		return orgPolicyParse{syntaxError: firstSemicolonPart(err.Error())}
	}
	issues := orgPolicyIssues(raw)
	if len(issues) > 0 {
		return orgPolicyParse{issues: issues}
	}
	jsonBytes, err := jsonMarshal(raw)
	if err != nil {
		return orgPolicyParse{issues: []schemas.Issue{{Message: err.Error()}}}
	}
	var policy schemas.OrgPolicy
	if err := decodeStrictJSON(jsonBytes, &policy); err != nil {
		return orgPolicyParse{issues: []schemas.Issue{{Message: err.Error()}}}
	}
	if schemaIssues := policy.Validate(); len(schemaIssues) > 0 {
		return orgPolicyParse{issues: schemaIssues}
	}
	return orgPolicyParse{policy: &policy}
}

// orgPolicyIssues checks the root-level unknown keys (the JSON decoder is
// lenient about them; zod's strictObject rejected them).
func orgPolicyIssues(raw any) []schemas.Issue {
	root, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var issues []schemas.Issue
	for _, key := range sortedAnyKeys(root) {
		switch key {
		case "version", "floors", "learning", "createdAt", "updatedAt":
		default:
			issues = append(issues, schemas.Issue{
				Path:    []string{key},
				Message: "Unrecognized key(s) in object: '" + key + "'",
			})
		}
	}
	if learning, ok := root["learning"].(map[string]any); ok {
		for _, key := range sortedAnyKeys(learning) {
			if key != "defaultMode" {
				issues = append(issues, schemas.Issue{
					Path:    []string{"learning", key},
					Message: "Unrecognized key(s) in object: '" + key + "'",
				})
			}
		}
	}
	return issues
}

// formatOrgPolicyIssues renders org policy issues the way the TS loader
// did (`path.join('.') || '<root>'` + message, joined by '; ').
func formatOrgPolicyIssues(issues []schemas.Issue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		label := "<root>"
		if len(issue.Path) > 0 {
			label = strings.Join(issue.Path, ".")
		}
		parts = append(parts, label+": "+issue.Message)
	}
	return strings.Join(parts, "; ")
}

// ------------------------------------------------------- shared helpers

// compareManifestDiagnostics is the total deterministic order for manifest
// diagnostics (line, column, code, file, message).
func compareManifestDiagnostics(a, b Diagnostic) int {
	if a.Line != b.Line {
		return a.Line - b.Line
	}
	if a.Column != b.Column {
		return a.Column - b.Column
	}
	if a.Code != b.Code {
		return strings.Compare(a.Code, b.Code)
	}
	if a.File != b.File {
		return strings.Compare(a.File, b.File)
	}
	return strings.Compare(a.Message, b.Message)
}

func sortManifestDiagnostics(diagnostics []Diagnostic) {
	sort.SliceStable(diagnostics, func(i, j int) bool {
		return compareManifestDiagnostics(diagnostics[i], diagnostics[j]) < 0
	})
}

func sortedStringKeys(m map[string][]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func modeStrings(modes []schemas.DocMode) []string {
	out := make([]string, len(modes))
	for i, mode := range modes {
		out[i] = string(mode)
	}
	return out
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ", ")
}
