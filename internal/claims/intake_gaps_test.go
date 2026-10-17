package claims

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWallClockNow(t *testing.T) {
	got := wallClockNow()

	assert.NotEmpty(t, got, "wallClockNow() must be non-empty")
	assert.True(t, strings.HasSuffix(got, "Z"), "wallClockNow() must end with the UTC 'Z' suffix, got: %s", got)
	assert.True(t, strings.Contains(got, "T"), "wallClockNow() must contain the 'T' date/time separator, got: %s", got)

	_, err := time.Parse(time.RFC3339, got)
	assert.NoError(t, err, "wallClockNow() must parse as RFC3339, got: %s", got)
}
