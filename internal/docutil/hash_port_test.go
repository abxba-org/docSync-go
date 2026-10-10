package docutil_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
)

// Ported from packages/core/test/state.test.ts — describe('P0-T12: contentHash').
func TestP0T12_ContentHashKnownDigests(t *testing.T) {
	// Known sha256 of the empty string.
	assert.Equal(t,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		docutil.ContentHash(""))
	// Known sha256 of "abc".
	assert.Equal(t,
		"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		docutil.ContentHash("abc"))
}

func TestP0T12_ContentHashDeterministicAndDiscriminating(t *testing.T) {
	// Deterministic for identical content.
	assert.Equal(t, docutil.ContentHash("state-bytes"), docutil.ContentHash("state-bytes"))
	// Distinguishes different content.
	assert.NotEqual(t, docutil.ContentHash("state-bytes"), docutil.ContentHash("state-bytez"))
	// The string and byte twins agree.
	assert.Equal(t,
		docutil.ContentHash("state-bytes"),
		docutil.ContentHashBytes([]byte("state-bytes")))
}
