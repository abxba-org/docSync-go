package testkit

// Ported from packages/core/src/testing/golden.ts (P0-T17): the golden-file
// harness. Asserts that a value matches a checked-in `.golden.json` snapshot
// under `<dir>/<name>.golden.json`.
//
// Canonicalization reuses the SAME canonical JSON as core state
// (docutil.CanonicalJSON, the port of core/src/state/canonical.ts) — single
// source of truth, so goldens are byte-stable across runs and key-order
// insensitive, exactly like committed state files. Non-JSON values fail closed
// BEFORE anything is written.
//
// Update modes (either enables rewriting):
//
//   - GoldenOptions.Update == true
//   - environment DOCSYNC_UPDATE_GOLDENS=1
//
// On mismatch (non-update mode) this returns a *GoldenAssertionError — the Go
// twin of the node:assert AssertionError the TS harness threw — carrying
// actual/expected so reporters can render a diff, with a deterministic message
// listing the first MaxDiffPaths differing JSON paths (keys walked in sorted
// order).
//
// Files on disk, fully deterministic, no network, no clock.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// GoldenSuffix is the suffix appended to the golden file name
// (TS GOLDEN_SUFFIX).
const GoldenSuffix = ".golden.json"

// UpdateGoldensEnv is the environment variable that enables update mode
// (same effect as GoldenOptions.Update) (TS UPDATE_GOLDENS_ENV).
const UpdateGoldensEnv = "DOCSYNC_UPDATE_GOLDENS"

// MaxDiffPaths is the first N differing paths reported in the mismatch
// message (deterministic cut) (TS MAX_DIFF_PATHS).
const MaxDiffPaths = 12

// GoldenOptions is the (dir, name, update) option bag of expectGolden
// (TS GoldenOptions).
type GoldenOptions struct {
	// Dir holds the golden files. Absolute, or resolved against the working
	// directory.
	Dir string
	// Name is the golden name WITHOUT suffix; a single safe filename segment
	// (`[A-Za-z0-9._-]+`).
	Name string
	// Update rewrites the golden on mismatch/absence (also enabled by
	// DOCSYNC_UPDATE_GOLDENS=1).
	Update bool
}

// GoldenAssertionError is the Go twin of the node:assert AssertionError the TS
// golden harness threw: a deterministic message plus the actual/expected
// values so reporters can render a diff.
type GoldenAssertionError struct {
	// Message is the deterministic, reporter-ready mismatch text.
	Message string
	// Actual is the value the caller asserted (nil when absent).
	Actual any
	// Expected is the value the golden file carried (nil when absent).
	Expected any
}

// Error renders the deterministic message (the node:assert message text).
func (e *GoldenAssertionError) Error() string { return e.Message }

// safeGoldenName matches one filename segment, safe across filesystems:
// `[A-Za-z0-9._-]+` — no separators, no traversal (TS assertSafeName).
var safeGoldenName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// assertSafeName refuses anything but a single safe filename segment.
func assertSafeName(name string) error {
	if !safeGoldenName.MatchString(name) {
		return fmt.Errorf(
			`golden name "%s" must be a single safe filename segment ([A-Za-z0-9._-], no '/'), got "%s"`,
			name, name)
	}
	return nil
}

// resolveGoldenDir resolves dir against the working directory when it is
// relative (TS resolveGoldenDir: isAbsolute ? dir : resolve(cwd, dir)).
func resolveGoldenDir(dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		// The working directory is always readable in practice; falling back
		// to a relative path keeps the failure visible at the call site.
		return filepath.Clean(dir)
	}
	return filepath.Clean(filepath.Join(cwd, dir))
}

// GoldenPathFor is the absolute path of the golden file for (dir, name)
// (TS goldenPathFor).
func GoldenPathFor(dir, name string) (string, error) {
	if err := assertSafeName(name); err != nil {
		return "", err
	}
	return filepath.Join(resolveGoldenDir(dir), name+GoldenSuffix), nil
}

// canonicalize renders value as the canonical JSON bytes the golden files
// carry (and the state store hashes): the value round-trips through generic
// JSON form with UseNumber first — so integers stay integral and key order is
// irrelevant — and docutil.CanonicalJSON then sorts keys recursively with
// JSON.stringify-compatible escaping. Fail closed on non-JSON values, before
// any file is touched.
func canonicalize(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// JSON.stringify does not escape <, > or &; neither may the canonical form.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return "", err
	}
	return docutil.CanonicalJSON(generic)
}

// parseJSONValue decodes canonical/raw JSON text into the generic form the
// differ walks (json.Number for numbers, exactly like JSON.parse feeding
// canonicalJson in the TS harness).
func parseJSONValue(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// jsonScalar renders a JSON scalar verbatim (TS JSON.stringify leaf).
func jsonScalar(value any) string {
	rendered, err := docutil.CanonicalJSON(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return rendered
}

// jsNumber renders a float the way TS JSON.stringify/Number() does: integral
// values print without a decimal point.
func jsNumber(value float64) string {
	if value == float64(int64(value)) && value < 1e15 && value > -1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// renderLeaf renders a leaf for the diff line: JSON scalar verbatim,
// structural summary otherwise (TS renderLeaf). Absence is signalled by the
// caller ("expected absent" / "actual absent"), so there is no `undefined`
// case here.
func renderLeaf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return jsonScalar(v)
	case bool:
		return jsonScalar(v)
	case json.Number:
		return v.String()
	case float64:
		return jsNumber(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case []any:
		return "array(" + strconv.Itoa(len(v)) + ")"
	case map[string]any:
		return "object(" + strconv.Itoa(len(v)) + " keys)"
	default:
		return fmt.Sprintf("unexpected-type(%T)", value)
	}
}

// sameJSONValue reports value identity the way the TS `expectedValue ===
// actualValue` guard does: scalars compare by value; containers (maps/slices)
// are distinct references in the decoded form, so identity is always false and
// the walk falls through to the structural branches. Values are always the
// output of parseJSONValue/canonicalize, so no other types occur here.
func sameJSONValue(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	default:
		return false
	}
}

// DiffPaths is the deterministic JSON-path diff between two canonical (already
// normalized) values, ported from TS diffPaths. Object keys are visited in
// sorted order; the first `limit` differences are returned as `"$"<path>`
// lines. Arrays are order-semantic (like state transition logs): index shifts
// are reported as real diffs. A limit <= 0 returns no lines.
func DiffPaths(expected, actual any, limit int) []string {
	out := []string{}
	if limit <= 0 {
		return out
	}
	var walk func(expectedValue, actualValue any, path string)
	walk = func(expectedValue, actualValue any, path string) {
		if len(out) >= limit {
			return
		}
		if sameJSONValue(expectedValue, actualValue) {
			return
		}

		expectedArray, expectedIsArray := expectedValue.([]any)
		actualArray, actualIsArray := actualValue.([]any)
		if expectedIsArray && actualIsArray {
			length := len(expectedArray)
			if len(actualArray) > length {
				length = len(actualArray)
			}
			for index := 0; index < length && len(out) < limit; index++ {
				elementPath := path + "[" + strconv.Itoa(index) + "]"
				switch {
				case index >= len(expectedArray):
					out = append(out, elementPath+": expected absent, actual "+renderLeaf(actualArray[index]))
				case index >= len(actualArray):
					out = append(out, elementPath+": expected "+renderLeaf(expectedArray[index])+", actual absent")
				default:
					walk(expectedArray[index], actualArray[index], elementPath)
				}
			}
			return
		}

		expectedObject, expectedIsObject := expectedValue.(map[string]any)
		actualObject, actualIsObject := actualValue.(map[string]any)
		if expectedIsObject && actualIsObject {
			keySet := make(map[string]struct{}, len(expectedObject)+len(actualObject))
			keys := make([]string, 0, len(expectedObject)+len(actualObject))
			for key := range expectedObject {
				if _, seen := keySet[key]; !seen {
					keySet[key] = struct{}{}
					keys = append(keys, key)
				}
			}
			for key := range actualObject {
				if _, seen := keySet[key]; !seen {
					keySet[key] = struct{}{}
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				if len(out) >= limit {
					return
				}
				keyPath := path + "." + key
				expectedChild, hasExpected := expectedObject[key]
				actualChild, hasActual := actualObject[key]
				switch {
				case !hasExpected:
					out = append(out, keyPath+": expected absent, actual "+renderLeaf(actualChild))
				case !hasActual:
					out = append(out, keyPath+": expected "+renderLeaf(expectedChild)+", actual absent")
				default:
					walk(expectedChild, actualChild, keyPath)
				}
			}
			return
		}

		out = append(out, path+": expected "+renderLeaf(expectedValue)+", actual "+renderLeaf(actualValue))
	}
	walk(expected, actual, "$")
	return out
}

// unlimitedDiffs is the "every difference" limit the mismatch summary walks
// with before cutting to MaxDiffPaths (TS Number.MAX_SAFE_INTEGER).
const unlimitedDiffs = int(^uint(0) >> 1)

// mismatchError builds the deterministic AssertionError twin: the file, the
// first MaxDiffPaths differing paths, the "… N more" cut, and the update-mode
// hint (TS mismatchError).
func mismatchError(file string, expectedValue, actualValue any) *GoldenAssertionError {
	totalDiffs := DiffPaths(expectedValue, actualValue, unlimitedDiffs)
	shown := totalDiffs
	if len(shown) > MaxDiffPaths {
		shown = shown[:MaxDiffPaths]
	}
	lines := []string{fmt.Sprintf("golden file mismatch: %s", file)}
	for _, line := range shown {
		lines = append(lines, "  "+line)
	}
	if total := len(totalDiffs); total > len(shown) {
		lines = append(lines, fmt.Sprintf("  ... %d more differing path(s)", total-len(shown)))
	}
	lines = append(lines, "update: set "+UpdateGoldensEnv+"=1 (or GoldenOptions.Update) to refresh goldens")
	return &GoldenAssertionError{
		Message:  strings.Join(lines, "\n"),
		Actual:   actualValue,
		Expected: expectedValue,
	}
}

// writeGoldenFile writes the canonical bytes plus exactly one trailing
// newline (POSIX-friendly; readers strip exactly one) and creates the
// directory when absent (TS writeGoldenFile).
func writeGoldenFile(file, canonical string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(canonical+"\n"), 0o644)
}

// isUpdateMode is the TS isUpdateMode: explicit update flag or the env var.
func isUpdateMode(update bool) bool {
	return update || os.Getenv(UpdateGoldensEnv) == "1"
}

// ExpectGolden asserts actual matches the golden snapshot
// `<dir>/<name>.golden.json`. Both sides are compared as canonical JSON bytes
// (recursive key sorting), so logically equal values in different key order
// match. Update mode rewrites the file; otherwise a mismatch returns a
// deterministic *GoldenAssertionError (TS expectGolden, which threw).
//
// Failure modes, in the TS order:
//   - the value is not JSON-serializable → error before any fs access;
//   - the golden file is absent and update mode is off → "golden file missing";
//   - the golden file is not valid JSON → fail closed, refusing to read;
//   - the values differ and update mode is off → the deterministic mismatch.
func ExpectGolden(actual any, opts GoldenOptions) error {
	file, err := GoldenPathFor(opts.Dir, opts.Name)
	if err != nil {
		return err
	}
	// Fail closed on non-JSON values BEFORE touching the filesystem.
	canonical, err := canonicalize(actual)
	if err != nil {
		return err
	}

	expectedRaw, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		expectedRaw = nil
	}

	if expectedRaw == nil {
		if !isUpdateMode(opts.Update) {
			return &GoldenAssertionError{
				Message: fmt.Sprintf("golden file missing: %s — set %s=1 to create it", file, UpdateGoldensEnv),
			}
		}
		return writeGoldenFile(file, canonical)
	}

	// Strip exactly one trailing newline (written by writeGoldenFile).
	expectedCanonical := string(expectedRaw)
	expectedCanonical = strings.TrimSuffix(expectedCanonical, "\n")
	if expectedCanonical == canonical {
		return nil
	}

	expectedValue, err := parseJSONValue(expectedCanonical)
	if err != nil {
		return fmt.Errorf("golden file %q is not valid JSON — refusing (fail closed): %w", file, err)
	}
	// Same logical value, different key order → still a match.
	expectedCanonicalBytes, err := canonicalize(expectedValue)
	if err != nil {
		return err
	}
	if expectedCanonicalBytes == canonical {
		return nil
	}

	if isUpdateMode(opts.Update) {
		return writeGoldenFile(file, canonical)
	}
	actualValue, err := parseJSONValue(canonical)
	if err != nil {
		return err
	}
	return mismatchError(file, expectedValue, actualValue)
}

// GoldenRead is the readGolden outcome (TS GoldenRead).
type GoldenRead struct {
	// Exists reports whether the golden file is present.
	Exists bool
	// Raw is the file text verbatim (one trailing newline), "" when absent.
	Raw string
	// Parsed is the parsed golden content, nil when the file is absent.
	Parsed any
}

// ReadGolden reads a golden file — no validation against actual, for absence
// assertions (TS readGolden). Invalid JSON fails closed rather than returning
// a half-read value.
func ReadGolden(dir, name string) (GoldenRead, error) {
	file, err := GoldenPathFor(dir, name)
	if err != nil {
		return GoldenRead{}, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			return GoldenRead{}, err
		}
		return GoldenRead{Exists: false, Raw: "", Parsed: nil}, nil
	}
	parsed, err := parseJSONValue(string(raw))
	if err != nil {
		return GoldenRead{}, fmt.Errorf(
			"golden file %q is not valid JSON — refusing to read (fail closed): %w", file, err)
	}
	return GoldenRead{Exists: true, Raw: string(raw), Parsed: parsed}, nil
}

// GoldenExists reports whether a golden file exists on disk — asserting
// absence without throwing (TS goldenExists).
func GoldenExists(dir, name string) (bool, error) {
	file, err := GoldenPathFor(dir, name)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
