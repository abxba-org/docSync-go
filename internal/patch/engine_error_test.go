package patch

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPatchEngineError(t *testing.T) {
	msg := "engine configuration error"
	err := newPatchEngineError(msg)

	assert.Equal(t, msg, err.Error())
	assert.Equal(t, docutil.CodeConfig, err.Code())

	unwrapped := err.Unwrap()
	require.NotNil(t, unwrapped)
	require.IsType(t, &docutil.Error{}, unwrapped)

	assert.Equal(t, docutil.CodeConfig, docutil.CodeOf(err))
	coded, ok := unwrapped.(*docutil.Error)
	require.True(t, ok)
	assert.Equal(t, msg, coded.Msg)
}
