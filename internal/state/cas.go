package state

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/DUDE616/docSync-go/internal/docutil"
)

// Ported from core/src/state/cas.ts: the content-addressed read/write layer
// under the whole state store. Every state file is read or written with an
// explicit content-hash expectation; there is NO delete or unlink API
// anywhere, and ENOENT is the only soft outcome (everything else is a hard
// failure, fail closed).

// StateRead is the outcome of a state-file read. ENOENT is the only soft
// outcome: Exists is false and every other field is its zero value, so a
// missing baseline and an existing one are distinguishable ONLY through
// Exists — never through a raw/hash sentinel.
type StateRead[T any] struct {
	Content T
	Raw     string
	Hash    string
	Exists  bool
}

// CasWriteResult is the outcome of a compare-and-swap write: ok:true with the
// hash of the bytes written, or ok:false with the expected-vs-actual hash pair
// (actual is nil when the file was absent — "expected to exist, did not").
type CasWriteResult struct {
	OK           bool
	Hash         string
	ExpectedHash *string
	ActualHash   *string
}

// CasWriteDetailed adds whether the write CREATED the file (the churn
// added-vs-updated split).
type CasWriteDetailed struct {
	OK           bool
	Hash         string
	Created      bool
	ExpectedHash *string
	ActualHash   *string
}

// Result projects the detailed outcome onto the plain CasWriteResult shape.
func (r CasWriteDetailed) Result() CasWriteResult {
	if r.OK {
		return CasWriteResult{OK: true, Hash: r.Hash}
	}
	return CasWriteResult{OK: false, ExpectedHash: r.ExpectedHash, ActualHash: r.ActualHash}
}

// isENOENT reports whether err is the file-not-found outcome — the ONLY fs
// error treated softly by this package.
func isENOENT(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// ReadStateFile reads one state file and parses its JSON content. ENOENT is
// the only soft outcome — an existing file that is not valid JSON is CORRUPT
// state and fails closed (EDOCSYNC_STATE_CORRUPT), never "treated as empty".
func ReadStateFile[T any](absPath string) (StateRead[T], error) {
	var empty StateRead[T]
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if isENOENT(err) {
			return empty, nil
		}
		return empty, err
	}
	if !json.Valid(raw) {
		return empty, StateCorruptError(`state file "%s" is not valid JSON — refusing to read (fail closed)`, absPath)
	}
	var content T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&content); err != nil {
		return empty, StateCorruptError(`state file "%s" is not valid JSON — refusing to read (fail closed)`, absPath)
	}
	return StateRead[T]{
		Content: content,
		Raw:     string(raw),
		Hash:    docutil.ContentHashBytes(raw),
		Exists:  true,
	}, nil
}

// readRawForCas reads raw bytes for a compare-and-swap: nil means absent.
func readRawForCas(absPath string) (*string, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		if isENOENT(err) {
			return nil, nil
		}
		return nil, err
	}
	current := string(raw)
	return &current, nil
}

// CasWriteFile is compare-and-swap on raw bytes: a nil expectedHash means
// "expect absent" (create), a hash means "expect exactly that content"
// (update). Anything else is ok:false with the expected-vs-actual pair.
func CasWriteFile(absPath, nextRaw string, expectedHash *string) (CasWriteResult, error) {
	detailed, err := CasWriteFileDetailed(absPath, nextRaw, expectedHash)
	if err != nil {
		return CasWriteResult{}, err
	}
	return detailed.Result(), nil
}

// CasWriteFileDetailed is the detailed twin of CasWriteFile, adding the
// created flag the churn hook needs.
func CasWriteFileDetailed(absPath, nextRaw string, expectedHash *string) (CasWriteDetailed, error) {
	currentRaw, err := readRawForCas(absPath)
	if err != nil {
		return CasWriteDetailed{}, err
	}
	var currentHash *string
	if currentRaw != nil {
		hash := docutil.ContentHash(*currentRaw)
		currentHash = &hash
		if expectedHash == nil || *currentHash != *expectedHash {
			return CasWriteDetailed{OK: false, ExpectedHash: expectedHash, ActualHash: currentHash}, nil
		}
	} else if expectedHash != nil {
		return CasWriteDetailed{OK: false, ExpectedHash: expectedHash, ActualHash: nil}, nil
	}
	if err := atomicWrite(absPath, nextRaw); err != nil {
		return CasWriteDetailed{}, err
	}
	return CasWriteDetailed{
		OK:      true,
		Hash:    docutil.ContentHash(nextRaw),
		Created: currentRaw == nil,
	}, nil
}

// atomicWrite replaces absPath with raw via a same-directory temp file plus
// rename, so a crash never leaves a half-written state file behind. Direct
// writeFile with truncation is never used on state (OD-R03 fail closed).
func atomicWrite(absPath, raw string) error {
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	tmpPath := absPath + ".tmp-" + randomToken()
	if err := os.WriteFile(tmpPath, []byte(raw), 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// randomToken names the temp file; uniqueness only needs to hold within the
// process lifetime of one write.
func randomToken() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
