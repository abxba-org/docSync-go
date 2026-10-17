package baselines_test

import (
	"testing"

	"github.com/DUDE616/docSync-go/internal/baselines"
	"github.com/stretchr/testify/require"
)

func TestCov_RenameFallback(t *testing.T) {
	var o = "src/payments/old.ts"
	r, e := baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
		Sources: []string{o},
		ChangedFiles: []baselines.InvalidationChangedFile{{
			Path: "src/payments/new.ts", Status: baselines.ChangeRenamed, OldPath: &o,
		}},
	})
	require.NoError(t, e)
	require.True(t, r.Invalidated)
}

func TestCov_RenameGlob(t *testing.T) {
	r, e := baselines.EvaluateSourceIntersection(baselines.EvaluateSourceIntersectionInput{
		Sources: []string{"src/payments/**"},
		ChangedFiles: []baselines.InvalidationChangedFile{{
			Path: "src/payments/new.ts", Status: baselines.ChangeRenamed, OldPath: strptr("src/payments/old.ts"),
		}},
	})
	require.NoError(t, e)
	require.True(t, r.Invalidated)
}
