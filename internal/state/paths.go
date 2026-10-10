package state

import (
	"regexp"
	"strings"
)

// Ported from core/src/state/paths.ts: the ONE place that maps docSync
// identifiers (doc paths, claim ids, run keys, ledger source keys) to
// `state/` relPaths and back. Paths are LITERALLY RECURSIVELY ENCODED — never
// decoded — because git consumed a visible separator once; every yielded
// relPath starts with "state/".
//
// The mapping is injective in both directions: a literal '_' escapes to
// "_u" BEFORE '/' flattens to "__", so `my_file.md` never lands on the stem
// path "__" would (D1 flattening)
// and the escape is fail-closed in both directions (`a_x`, a dangling `_`,
// and any non-safe-charset byte refuse to decode rather than being repaired).

const STATE_FILE_SUFFIX = ".json"

// stateRelPrefix is the ownership prefix every state relPath carries.
const stateRelPrefix = "state/"

// safeSegment matches one path-segment component: `[A-Za-z0-9._-]+`. Both
// encoders and decoders fail closed on anything outside it.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// EncodeStateSegment encodes ONE safe-charset segment: every literal `_`
// becomes `_u`. Because the escape is applied FIRST, it never manufactures
// path-separator runs — `my_file.md` encodes to `my_ufile.md` and
// `docs/__2__.md` encodes to `docs___u_u2_u_u.md`, and both still decode to
// themselves.
func EncodeStateSegment(segment string) (string, error) {
	if len(segment) == 0 {
		return "", StateKeyError("path segment is empty")
	}
	if !safeSegment.MatchString(segment) {
		return "", StateKeyError(`path segment "%s" is outside [A-Za-z0-9._-] — not encodable`, segment)
	}
	return strings.ReplaceAll(segment, "_", "_u"), nil
}

// DecodeStateSegment is the exact inverse of EncodeStateSegment, fail-closed:
// `_x` (an unknown escape) and a dangling `_` are REFUSED rather than
// repaired, so a malformed stem can never be mistaken for a valid segment.
func DecodeStateSegment(encoded string) (string, error) {
	if len(encoded) == 0 {
		return "", StateKeyError("encoded path segment is empty")
	}
	var segment strings.Builder
	for i := 0; i < len(encoded); i++ {
		char := encoded[i]
		if char != '_' {
			segment.WriteByte(char)
			continue
		}
		if i+1 < len(encoded) && encoded[i+1] == 'u' {
			segment.WriteByte('_')
			i++
			continue
		}
		return "", StateKeyError(`encoded path segment "%s" has a malformed '_' escape at index %d`, encoded, i)
	}
	decoded := segment.String()
	// The safe charset is enforced AFTER decoding: any byte outside
	// [A-Za-z0-9._-] (including invalid UTF-8) refuses to decode.
	if !safeSegment.MatchString(decoded) {
		return "", StateKeyError(`decoded path segment "%s" is outside [A-Za-z0-9._-]`, decoded)
	}
	return decoded, nil
}

// assertEncodableDocPath validates a repo-relative doc path and returns its
// segments; a NULL byte, a backslash, an absolute path, `..` traversal, an
// empty segment or any non-safe-charset segment refuses (fail closed).
func assertEncodableDocPath(docPath string) ([]string, error) {
	if len(docPath) == 0 {
		return nil, StateKeyError("doc path is empty")
	}
	if strings.Contains(docPath, "\\") {
		return nil, StateKeyError(`doc path "%s" contains backslashes — use POSIX '/'`, docPath)
	}
	if strings.HasPrefix(docPath, "/") {
		return nil, StateKeyError(`doc path "%s" is absolute — repo-relative required`, docPath)
	}
	if strings.Contains(docPath, "\x00") {
		return nil, StateKeyError(`doc path "%s" contains NUL`, docPath)
	}
	normalized := docPath
	for strings.HasPrefix(normalized, "./") {
		normalized = normalized[len("./"):]
	}
	segments := strings.Split(normalized, "/")
	for _, segment := range segments {
		if segment == ".." {
			return nil, StateKeyError(`doc path "%s" contains '..' traversal`, docPath)
		}
	}
	for _, segment := range segments {
		if len(segment) == 0 {
			return nil, StateKeyError(`doc path "%s" has an empty segment`, docPath)
		}
	}
	for _, segment := range segments {
		if !safeSegment.MatchString(segment) {
			return nil, StateKeyError(`doc path "%s" has a segment outside [A-Za-z0-9._-] — not encodable`, docPath)
		}
	}
	return segments, nil
}

// EncodeStateDocPath flattens a repo-relative doc path to a single filename
// stem: each segment is escaped with EncodeStateSegment, then the segments are
// joined with "__". The result contains no '/' and decodes to exactly the
// input.
func EncodeStateDocPath(docPath string) (string, error) {
	segments, err := assertEncodableDocPath(docPath)
	if err != nil {
		return "", err
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

// DecodeStateDocPath is the exact inverse of EncodeStateDocPath, fail-closed.
func DecodeStateDocPath(encoded string) (string, error) {
	if len(encoded) == 0 {
		return "", StateKeyError("encoded doc path is empty")
	}
	parts := strings.Split(encoded, "__")
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

// stateRelPath builds a `state/<subdir>/<filename>` relPath.
func stateRelPath(subdir, filename string) string {
	return stateRelPrefix + subdir + "/" + filename
}

// BaselineRelPath is the routing for baseline records (ARCH §6.1, "D1
// flattening"): `state/baselines/<encoded doc path>.json`.
func BaselineRelPath(docPath string) (string, error) {
	encoded, err := EncodeStateDocPath(docPath)
	if err != nil {
		return "", err
	}
	return stateRelPath("baselines", encoded+STATE_FILE_SUFFIX), nil
}

// ClaimRelPath is the routing for claim records: `state/claims/<claim id>.json`.
func ClaimRelPath(claimID string) (string, error) {
	if len(claimID) == 0 {
		return "", StateKeyError("claim id is empty")
	}
	if !safeSegment.MatchString(claimID) || strings.Contains(claimID, "..") {
		return "", StateKeyError(`claim id "%s" is not filename-safe ([A-Za-z0-9._-], no '..')`, claimID)
	}
	return stateRelPath("claims", claimID+STATE_FILE_SUFFIX), nil
}

// DocLedgerRelPath is the routing for merge-ledger records keyed by their
// source key (P6-T18): `state/merge-ledger/<encoded source key>.json`.
func DocLedgerRelPath(sourceKey string) (string, error) {
	encoded, err := EncodeStateDocPath(sourceKey)
	if err != nil {
		return "", err
	}
	return stateRelPath("merge-ledger", encoded+STATE_FILE_SUFFIX), nil
}

// DocStateRelPath is the routing for doc-lifecycle records (ARCH §6.2):
// `state/docs/<encoded doc path>.json`.
func DocStateRelPath(docPath string) (string, error) {
	encoded, err := EncodeStateDocPath(docPath)
	if err != nil {
		return "", err
	}
	return stateRelPath("docs", encoded+STATE_FILE_SUFFIX), nil
}

// AuditCursorRelPath is the routing for the Loop-B audit cursor. The file
// carries NO extension by design (TS parity: `state/audit-cursor`, never
// `state/audit-cursor.json`).
func AuditCursorRelPath() string {
	return stateRelPrefix + "audit-cursor"
}

// RunRecordRelPath is the routing for run records: `state/runs/<filename>.json`
// where the filename comes from RunRecordFilename.
func RunRecordRelPath(key RunKey) (string, error) {
	filename, err := RunRecordFilename(key)
	if err != nil {
		return "", err
	}
	return stateRelPath("runs", filename), nil
}
