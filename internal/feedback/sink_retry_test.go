package feedback

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTruncateForMessage(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		assert.Equal(t, "", truncateForMessage("", 5))
	})
	t.Run("shorter than limit", func(t *testing.T) {
		assert.Equal(t, "abc", truncateForMessage("abc", 5))
	})
	t.Run("longer than limit", func(t *testing.T) {
		assert.Equal(t, "ab", truncateForMessage("abcd", 2))
	})
	t.Run("exact limit", func(t *testing.T) {
		assert.Equal(t, "abc", truncateForMessage("abc", 3))
	})
}

func TestMarshalRow(t *testing.T) {
	out, err := marshalRow(map[string]string{"a": "<tag>"})
	require.NoError(t, err)
	assert.Contains(t, out, "<tag>")
	assert.NotContains(t, out, "\\u003c")
}

func TestMarshalRowCompact(t *testing.T) {
	out, err := marshalRowCompact(map[string]string{"a": "b"})
	require.NoError(t, err)
	assert.False(t, strings.HasSuffix(string(out), "\n"))
}
