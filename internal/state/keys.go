package state

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/schemas"
)

// RunKey is the run-record idempotency key (RUN_KEY_SCHEMA): repo × base_sha ×
// head_sha × manifest_version × policy_version. Re-exported from
// internal/schemas so state callers name one type.
type RunKey = schemas.RunKey

// safeComponent matches the key-component charset: `[A-Za-z0-9._-]+`.
var safeComponent = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SameRunKey is strict 5-tuple identity — nothing is normalized, defaulted or
// version-tolerated. Runs re-assert full key identity on every read and
// upsert, so a real conflict (same filename, different run payload) never
// silently overwrites.
func SameRunKey(a, b RunKey) bool {
	return a.Repo == b.Repo &&
		a.BaseSHA == b.BaseSHA &&
		a.HeadSHA == b.HeadSHA &&
		a.ManifestVersion == b.ManifestVersion &&
		a.PolicyVersion == b.PolicyVersion
}

// assertComponent refuses an empty or unsafe key component (fail closed).
func assertComponent(value, field string) error {
	if len(value) == 0 {
		return StateKeyError(`run key field "%s" is empty`, field)
	}
	if !safeComponent.MatchString(value) {
		return StateKeyError(`run key field "%s"="%s" is not filename-safe ([A-Za-z0-9._-]); refusing (fail closed)`, field, value)
	}
	return nil
}

// encodeRunRepo encodes the repo against the SAME escape that separates
// segments, so `acme/corp` (`acme__corp`) never lands on the stem reserved
// for the literal `acme__corp` (`acme_u_ucorp`).
func encodeRunRepo(repo string) (string, error) {
	segments := strings.Split(repo, "/")
	for _, segment := range segments {
		if len(segment) == 0 || !safeComponent.MatchString(segment) {
			return "", StateKeyError(`run key repo "%s" is not filename-safe ([A-Za-z0-9._-] segments, no '#'); refusing (fail closed)`, repo)
		}
	}
	encoded := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped, err := EncodeStateSegment(segment)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, escaped)
	}
	return strings.Join(encoded, "__"), nil
}

// DecodeRunRecordRepo is the exact inverse of the repo component of
// RunRecordFilename: `__`-separated parts, each decoded with
// DecodeStateSegment, joined with '/'.
func DecodeRunRecordRepo(encodedRepo string) (string, error) {
	parts := strings.Split(encodedRepo, "__")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		decoded, err := DecodeStateSegment(part)
		if err != nil {
			return "", err
		}
		segments = append(segments, decoded)
	}
	return strings.Join(segments, "/"), nil
}

// RunRecordFilename renders the idempotent filename for one run key:
// `state/runs/<repo>#<base>..<head>#m<mv>#p<pv>.json`.
func RunRecordFilename(key RunKey) (string, error) {
	if err := assertComponent(key.BaseSHA, "base_sha"); err != nil {
		return "", err
	}
	if err := assertComponent(key.HeadSHA, "head_sha"); err != nil {
		return "", err
	}
	repo, err := encodeRunRepo(key.Repo)
	if err != nil {
		return "", err
	}
	return repo + "#" + key.BaseSHA + ".." + key.HeadSHA +
		"#m" + strconv.Itoa(key.ManifestVersion) +
		"#p" + strconv.Itoa(key.PolicyVersion) + ".json", nil
}
