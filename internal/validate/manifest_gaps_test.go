package validate

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func TestFloorMaxLadderGaps(t *testing.T) {
	t.Run("nil MaxMode returns -1", func(t *testing.T) {
		v := FloorViolation{Floor: schemas.OrgFloor{}}
		assert.Equal(t, -1, floorMaxLadder(v))
	})

	t.Run("resolved mode auto maps to modeLadder rank > -1", func(t *testing.T) {
		auto := schemas.DocModeAuto
		v := FloorViolation{Floor: schemas.OrgFloor{MaxMode: &auto}}
		assert.Greater(t, floorMaxLadder(v), -1)
		assert.Equal(t, modeLadder[schemas.DocModeAuto], floorMaxLadder(v))
	})

	t.Run("locked returns modeLadder locked", func(t *testing.T) {
		locked := schemas.DocModeLocked
		v := FloorViolation{Floor: schemas.OrgFloor{MaxMode: &locked}}
		assert.Equal(t, modeLadder[schemas.DocModeLocked], floorMaxLadder(v))
	})
}
