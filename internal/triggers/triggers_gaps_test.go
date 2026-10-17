package triggers_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/triggers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownTriggerError_UnwrapReturnsCodedError(t *testing.T) {
	err := triggers.NewUnknownTriggerError([]string{"pay_flow_changed"}, triggers.TriggerDirectionTriggers)

	unwrapped := err.Unwrap()

	require.NotNil(t, unwrapped, "Unwrap() must return a non-nil error")

	var coded *docutil.Error
	require.True(t, errors.As(unwrapped, &coded), "unwrapped error must be a *docutil.Error")

	assert.Equal(t, "EDOCSYNC_TRIGGER_UNKNOWN", coded.Code)
	assert.Contains(t, coded.Msg, `unknown trigger value "pay_flow_changed"`)
}
