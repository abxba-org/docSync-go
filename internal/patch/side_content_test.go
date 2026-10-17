package patch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSideContentForRegion(t *testing.T) {
	t.Run("no edits in region returns base lines", func(t *testing.T) {
		got := sideContentForRegion(nil, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"b", "c"}, got)
	})

	t.Run("single replacement edit inside region", func(t *testing.T) {
		edits := []Edit{{From: 1, To: 3, Lines: []string{"X", "Y"}}}
		got := sideContentForRegion(edits, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"X", "Y"}, got)
	})

	t.Run("pure insertion inside region", func(t *testing.T) {
		edits := []Edit{{From: 2, To: 2, Lines: []string{"INSERT"}}}
		got := sideContentForRegion(edits, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"b", "INSERT", "c"}, got)
	})

	t.Run("edit before region is skipped", func(t *testing.T) {
		edits := []Edit{{From: 5, To: 6, Lines: []string{"Z"}}}
		got := sideContentForRegion(edits, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"b", "c"}, got)
	})

	t.Run("partially overlapping edit", func(t *testing.T) {
		edits := []Edit{{From: 0, To: 2, Lines: []string{"P", "Q"}}}
		got := sideContentForRegion(edits, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"P", "Q", "c"}, got)
	})

	t.Run("multiple edits in region", func(t *testing.T) {
		edits := []Edit{
			{From: 1, To: 2, Lines: []string{"B1"}},
			{From: 2, To: 3, Lines: []string{"C1"}},
		}
		got := sideContentForRegion(edits, []string{"a", "b", "c", "d"}, 1, 3)
		assert.Equal(t, []string{"B1", "C1"}, got)
	})
}
