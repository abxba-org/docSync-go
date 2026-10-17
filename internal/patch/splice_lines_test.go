package patch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpliceLines(t *testing.T) {
	// 1. Normal splice: a=["a","b","c","d"], start=1, removeCount=2, replacement=["x","y"] → ["a","x","y","d"]
	t.Run("normal splice", func(t *testing.T) {
		a := []string{"a", "b", "c", "d"}
		result := spliceLines(a, 1, 2, []string{"x", "y"})
		assert.Equal(t, []string{"a", "x", "y", "d"}, result)
	})

	// 2. Delete only (no replacement): a=["a","b","c"], start=1, removeCount=1, replacement=nil → ["a","c"]
	t.Run("delete only", func(t *testing.T) {
		a := []string{"a", "b", "c"}
		result := spliceLines(a, 1, 1, nil)
		assert.Equal(t, []string{"a", "c"}, result)
	})

	// 3. Insert only (removeCount=0): a=["a","c"], start=1, removeCount=0, replacement=["b"] → ["a","b","c"]
	t.Run("insert only", func(t *testing.T) {
		a := []string{"a", "c"}
		result := spliceLines(a, 1, 0, []string{"b"})
		assert.Equal(t, []string{"a", "b", "c"}, result)
	})

	// 4. start < 0 clamped to 0: a=["a","b"], start=-5, removeCount=1, replacement=["x"] → ["x","b"]
	t.Run("start negative clamp", func(t *testing.T) {
		a := []string{"a", "b"}
		result := spliceLines(a, -5, 1, []string{"x"})
		assert.Equal(t, []string{"x", "b"}, result)
	})

	// 5. start > len(a) clamped to len(a): a=["a"], start=10, removeCount=0, replacement=["b"] → ["a","b"]
	t.Run("start beyond length clamp", func(t *testing.T) {
		a := []string{"a"}
		result := spliceLines(a, 10, 0, []string{"b"})
		assert.Equal(t, []string{"a", "b"}, result)
	})

	// 6. removeCount clamps to available: a=["a","b"], start=0, removeCount=100, replacement=["x"] → ["x"]
	t.Run("removeCount clamp", func(t *testing.T) {
		a := []string{"a", "b"}
		result := spliceLines(a, 0, 100, []string{"x"})
		assert.Equal(t, []string{"x"}, result)
	})

	// 7. Empty input, start=0, removeCount=0, replacement=["x"] → ["x"]
	t.Run("empty input", func(t *testing.T) {
		var a []string
		result := spliceLines(a, 0, 0, []string{"x"})
		assert.Equal(t, []string{"x"}, result)
	})

	// 8. Empty replacement at end: a=["a","b"], start=2, removeCount=0, replacement=[] → ["a","b"]
	t.Run("empty replacement at end", func(t *testing.T) {
		a := []string{"a", "b"}
		result := spliceLines(a, 2, 0, []string{})
		assert.Equal(t, []string{"a", "b"}, result)
	})
}
