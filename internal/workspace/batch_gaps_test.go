package workspace

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
)

func TestNewBatchConfigError(t *testing.T) {
	err := NewBatchConfigError("boom")

	assert.Equal(t, CodeBatchConfig, err.Code())
	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	assert.NotNil(t, err.Unwrap())
	assert.Contains(t, err.Unwrap().Error(), "boom")
}
