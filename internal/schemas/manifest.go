package schemas

import "strings"

// DocMode is one of five mutability modes (ARCH §11.2).
type DocMode string

// DOC_MODE_VALUES lists the five mutability modes (ARCH §11.2).
const (
	DocModeAuto      DocMode = "auto"
	DocModeSuggest   DocMode = "suggest"
	DocModeLocked    DocMode = "locked"
	DocModeGenerated DocMode = "generated"
	DocModeHuman     DocMode = "human"
)

// DocModeValues lists all modes in canonical order.
var DocModeValues = []DocMode{DocModeAuto, DocModeSuggest, DocModeLocked, DocModeGenerated, DocModeHuman}

// IsValidDocMode reports whether m is a known mode.
func IsValidDocMode(m string) bool {
	for _, v := range DocModeValues {
		if string(v) == m {
			return true
		}
	}
	return false
}

// MANIFEST_ENTRY_FIELDS lists entry fields in canonical order.
var ManifestEntryFields = []string{"path", "mode", "sources", "triggers", "ignore"}

// IsValidManifestPath: nonempty, relative, POSIX-style, no `..` segments.
func IsValidManifestPath(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "/") {
		return false
	}
	if strings.Contains(path, "\\") {
		return false
	}
	if strings.Contains(path, "\x00") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

const manifestPathMessage = "document path must be a nonempty relative POSIX path without '..' segments or backslashes"

// ManifestEntry is one manifest entry. Mode is optional — resolved later via
// defaults/precedence.
type ManifestEntry struct {
	Path     string   `json:"path"`
	Mode     *DocMode `json:"mode,omitempty"`
	Sources  []string `json:"sources"`
	Triggers []string `json:"triggers,omitempty"`
	Ignore   []string `json:"ignore,omitempty"`
}

// Validate checks the entry against the manifest schema (strict).
func (e *ManifestEntry) Validate() []Issue {
	var issues []Issue
	if !IsValidManifestPath(e.Path) {
		Collect(&issues, &Issue{Path: []string{"path"}, Message: manifestPathMessage})
	}
	if e.Mode != nil && !IsValidDocMode(string(*e.Mode)) {
		Collect(&issues, &Issue{Path: []string{"mode"}, Message: "Invalid enum value. Expected 'auto' | 'suggest' | 'locked' | 'generated' | 'human', received '" + string(*e.Mode) + "'"})
	}
	if len(e.Sources) < 1 {
		Collect(&issues, &Issue{Path: []string{"sources"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i, s := range e.Sources {
		Collect(&issues, RequiredString(s, 1, []string{"sources", itoa(i)}))
	}
	for i, t := range e.Triggers {
		Collect(&issues, RequiredString(t, 1, []string{"triggers", itoa(i)}))
	}
	for i, t := range e.Ignore {
		Collect(&issues, RequiredString(t, 1, []string{"ignore", itoa(i)}))
	}
	return issues
}

// ManifestDefaults holds default mode resolution.
type ManifestDefaults struct {
	Mode *DocMode `json:"mode,omitempty"`
}

// Validate checks the defaults block.
func (d *ManifestDefaults) Validate() []Issue {
	var issues []Issue
	if d.Mode != nil && !IsValidDocMode(string(*d.Mode)) {
		Collect(&issues, &Issue{Path: []string{"mode"}, Message: "Invalid enum value for defaults.mode"})
	}
	return issues
}

// Manifest is the root manifest. Version is any positive integer at the
// schema level; consumers enforce supported versions via the registry
// (fail-closed per D8).
type Manifest struct {
	Version   int              `json:"version"`
	Documents []ManifestEntry  `json:"documents"`
	Defaults  *ManifestDefaults `json:"defaults,omitempty"`
}

// Validate checks the root manifest against the schema (strict).
func (m *Manifest) Validate() []Issue {
	var issues []Issue
	if m.Version <= 0 {
		Collect(&issues, &Issue{Path: []string{"version"}, Message: "Number must be greater than 0"})
	}
	if len(m.Documents) < 1 {
		Collect(&issues, &Issue{Path: []string{"documents"}, Message: "Array must contain at least 1 element(s)"})
	}
	for i := range m.Documents {
		for _, iss := range m.Documents[i].Validate() {
			iss.Path = append([]string{"documents", itoa(i)}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	if m.Defaults != nil {
		for _, iss := range m.Defaults.Validate() {
			iss.Path = append([]string{"defaults"}, iss.Path...)
			issues = append(issues, iss)
		}
	}
	return issues
}

// ParseManifest strictly parses and validates YAML/JSON manifest bytes.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := decodeYAMLOrJSON(data, &m); err != nil {
		return nil, err
	}
	if issues := m.Validate(); len(issues) > 0 {
		return nil, NewValidationError(issues...)
	}
	return &m, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
