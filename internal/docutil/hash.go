package docutil

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash returns the sha256 hex digest of content (UTF-8 encoded),
// ported from core/src/state/hash.ts. CAS hashes, content-addressed
// verification and every "same bytes?" comparison in docSync go through this
// single helper — never ad-hoc marshaling plus hashing at call sites.
//
// Known digests pinned by the TS suite: ContentHash("") ==
// e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 and
// ContentHash("abc") ==
// ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad.
func ContentHash(content string) string {
	return ContentHashBytes([]byte(content))
}

// ContentHashBytes returns the sha256 hex digest of raw bytes — the byte
// twin of ContentHash for callers that already hold []byte.
func ContentHashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
