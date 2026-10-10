package docutil_test

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from the TS error-class conventions (packages/core/src/state/errors.ts
// and the EDocsync* classes): every error carries a distinguishable code and a
// preserved message format, rendered as "CODE: msg" or "CODE at path: msg".
func TestCodedErrorConstructorsCarryDistinctCodes(t *testing.T) {
	cases := []struct {
		name string
		err  *docutil.Error
		code string
	}{
		{"config", docutil.ConfigError("bad"), docutil.CodeConfig},
		{"manifest", docutil.ManifestError("bad"), docutil.CodeManifest},
		{"validation", docutil.ValidationError("bad"), docutil.CodeValidation},
		{"state", docutil.StateError("bad"), docutil.CodeState},
		{"stateKey", docutil.StateKeyError("bad"), docutil.CodeStateKey},
		{"stateCAS", docutil.StateCASError("bad"), docutil.CodeStateCAS},
		{"schema", docutil.SchemaError("bad"), docutil.CodeSchema},
		{"git", docutil.GitError("bad"), docutil.CodeGit},
		{"model", docutil.ModelError("bad"), docutil.CodeModel},
		{"verify", docutil.VerifyError("bad"), docutil.CodeVerify},
		{"guard", docutil.GuardError("bad"), docutil.CodeGuard},
		{"io", docutil.IOError("bad"), docutil.CodeIO},
		{"internal", docutil.InternalError("bad"), docutil.CodeInternal},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, tc.err.Code)
			assert.Equal(t, tc.code, docutil.CodeOf(tc.err))
			assert.Equal(t, tc.code+": bad", tc.err.Error())
			assert.False(t, seen[tc.code], "codes must be distinct")
			seen[tc.code] = true
		})
	}
}

func TestCodedErrorNewAndFormatting(t *testing.T) {
	// New builds a coded error with a verbatim message.
	err := docutil.New(docutil.CodeIO, "disk on fire")
	assert.Equal(t, docutil.CodeIO, docutil.CodeOf(err))
	assert.Equal(t, "EDOCSYNC_IO: disk on fire", err.Error())

	// Newf formats through the convenience path too.
	formatted := docutil.Newf(docutil.CodeVerify, "check %s failed (%d)", "links", 3)
	assert.Equal(t, "EDOCSYNC_VERIFY: check links failed (3)", formatted.Error())
}

func TestCodedErrorCodeOfNonCoded(t *testing.T) {
	// Plain errors report no code (never panic, never "").
	assert.Equal(t, "", docutil.CodeOf(errors.New("plain")))
	assert.Equal(t, "", docutil.CodeOf(nil))
}

func TestCodedErrorAtWrapAndRendering(t *testing.T) {
	// With a path: "CODE at path: msg"; the path renders dot-joined.
	at := docutil.ConfigError("bad key").At("documents", "3", "title")
	assert.Equal(t, "EDOCSYNC_CONFIG at documents.3.title: bad key", at.Error())
	assert.Equal(t, []string{"documents", "3", "title"}, at.Path)

	// At/Wrap return copies: the original stays untouched.
	base := docutil.ManifestError("base")
	base.At("a").Wrap(errors.New("cause"))
	assert.Empty(t, base.Path)
	assert.NoError(t, base.Unwrap())

	// Wrap preserves the cause for errors.Is/As.
	cause := errors.New("root cause")
	wrapped := docutil.StateError("outer").Wrap(cause)
	assert.ErrorIs(t, wrapped, cause)
	var coded *docutil.Error
	require.True(t, errors.As(wrapped, &coded))
	assert.Equal(t, docutil.CodeState, coded.Code)

	// At composes with Wrap.
	both := docutil.ValidationError("v").At("field").Wrap(cause)
	assert.Equal(t, "EDOCSYNC_VALIDATION at field: v", both.Error())
	assert.ErrorIs(t, both, cause)
}
