package patch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJoinLikeBasis(t *testing.T) {
	// 1. Empty lines → ""
	t.Run("empty lines", func(t *testing.T) {
		result := joinLikeBasis([]string{}, "x")
		assert.Equal(t, "", result)
	})

	// 2. Lines with basisContent ending in "\n": lines=["a","b"], basisContent="x\n" → "a\nb\n"
	t.Run("lines basis ends newline", func(t *testing.T) {
		result := joinLikeBasis([]string{"a", "b"}, "x\n")
		assert.Equal(t, "a\nb\n", result)
	})

	// 3. Lines with basisContent NOT ending in "\n": lines=["a","b"], basisContent="x" → "a\nb"
	t.Run("lines basis no newline", func(t *testing.T) {
		result := joinLikeBasis([]string{"a", "b"}, "x")
		assert.Equal(t, "a\nb", result)
	})

	// 4. Single line, basis ends with newline: lines=["hello"], basisContent="world\n" → "hello\n"
	t.Run("single line basis ends newline", func(t *testing.T) {
		result := joinLikeBasis([]string{"hello"}, "world\n")
		assert.Equal(t, "hello\n", result)
	})

	// 5. Single line, basis no newline: lines=["hello"], basisContent="world" → "hello"
	t.Run("single line basis no newline", func(t *testing.T) {
		result := joinLikeBasis([]string{"hello"}, "world")
		assert.Equal(t, "hello", result)
	})

	// 6. Multiple lines, empty basis: lines=["a","b","c"], basisContent="" → "a\nb\nc"
	t.Run("multiple lines empty basis", func(t *testing.T) {
		result := joinLikeBasis([]string{"a", "b", "c"}, "")
		assert.Equal(t, "a\nb\nc", result)
	})
}
