package githubapp

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DUDE616/docSync-go/internal/gitpr"
)

func TestCOV_GITHUBAPP_InternalHelpers(t *testing.T) {
	t.Run("isDuplicateCreateError: nil err -> false", func(t *testing.T) {
		assert.False(t, isDuplicateCreateError(nil))
	})
	t.Run("isDuplicateCreateError: validation error -> true", func(t *testing.T) {
		assert.True(t, isDuplicateCreateError(gitpr.NewGitHubValidationError("x", gitpr.GitHubRestErrorInit{Status: intPtr(422)})))
	})
	t.Run("isDuplicateCreateError: other error -> false", func(t *testing.T) {
		assert.False(t, isDuplicateCreateError(errors.New("boom")))
	})

	t.Run("isSelfTrigger: all four rules", func(t *testing.T) {
		assert.True(t, isSelfTrigger(strPtrP("bot"), nil, "bot"))               // actor identity only
		assert.True(t, isSelfTrigger(strPtrP("bot"), strPtrP("feature"), "bot"))   // actor identity
		assert.True(t, isSelfTrigger(nil, strPtrP("docsync/x"), "bot"))            // branch prefix, no actor
		assert.False(t, isSelfTrigger(strPtrP("human"), strPtrP("feature"), "bot")) // neither
	})

	t.Run("installationIDOf: all id shapes", func(t *testing.T) {
		assert.Equal(t, int64(42), installationIDOf(map[string]any{"installation": map[string]any{"id": float64(42)}}))
		assert.Equal(t, float64(42.5), installationIDOf(map[string]any{"installation": map[string]any{"id": float64(42.5)}}))
		assert.Nil(t, installationIDOf(map[string]any{"installation": map[string]any{"id": ""}}))
		assert.Equal(t, "abc", installationIDOf(map[string]any{"installation": map[string]any{"id": "abc"}}))
		assert.Nil(t, installationIDOf(map[string]any{"installation": map[string]any{"id": true}}))
		assert.Nil(t, installationIDOf(map[string]any{}))
		assert.Nil(t, installationIDOf(map[string]any{"installation": nil}))
	})

	t.Run("branchFromRef: all ref shapes", func(t *testing.T) {
		assert.Nil(t, branchFromRef(nil))
		assert.Nil(t, branchFromRef(strPtrP("")))
		assert.Nil(t, branchFromRef(strPtrP("main")))
		assert.Nil(t, branchFromRef(strPtrP("refs/heads/")))
		assert.Equal(t, "dev", *branchFromRef(strPtrP("refs/heads/dev")))
	})

	t.Run("jsonWhole: all number kinds", func(t *testing.T) {
		v, ok := jsonWhole(float64(3)); assert.True(t, ok); assert.Equal(t, int64(3), v)
		v, ok = jsonWhole(float64(3.5)); assert.False(t, ok)
		v, ok = jsonWhole(3); assert.True(t, ok); assert.Equal(t, int64(3), v)
		v, ok = jsonWhole(int64(3)); assert.True(t, ok); assert.Equal(t, int64(3), v)
		v, ok = jsonWhole("3"); assert.False(t, ok)
		v, ok = jsonWhole(nil); assert.False(t, ok)
	})

	t.Run("asStringPayloadNested: all path shapes", func(t *testing.T) {
		assert.Nil(t, asStringPayloadNested(map[string]any{}))                        // empty path
		assert.Equal(t, "v", *asStringPayloadNested(map[string]any{"a": map[string]any{"b": "v"}}, "a", "b"))
		assert.Nil(t, asStringPayloadNested(map[string]any{"a": map[string]any{}}, "a", "b")) // intermediate nil
		assert.Nil(t, asStringPayloadNested(map[string]any{"a": map[string]any{"b": 7}}, "a", "b")) // final non-string
		assert.Nil(t, asStringPayloadNested(map[string]any{"a": "notobj"}, "a", "b"))
	})

	t.Run("parsePullRequestContext: all shapes", func(t *testing.T) {
		pr, ok := parsePullRequestContext(map[string]any{})
		assert.False(t, ok); assert.Nil(t, pr)
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{}})
		assert.False(t, ok)
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{"head": map[string]any{"sha": "abc", "ref": "feat"}}})
		assert.False(t, ok) // missing number
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{"number": 1, "head": map[string]any{"ref": "feat"}}})
		assert.False(t, ok) // missing head sha
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{"number": 1, "head": map[string]any{"sha": "abc"}}})
		assert.False(t, ok) // missing head branch
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{"number": 1.5, "head": map[string]any{"sha": "abc", "ref": "feat"}}})
		assert.False(t, ok) // non-whole number
		pr, ok = parsePullRequestContext(map[string]any{"pull_request": map[string]any{"number": 1, "head": map[string]any{"sha": "abc", "ref": "feat"}, "base": map[string]any{"ref": "main"}}})
		require.True(t, ok); assert.Equal(t, 1, pr.Number); assert.Equal(t, "main", *pr.BaseBranch)
	})

	t.Run("parsePushContext: all shapes", func(t *testing.T) {
		assert.Nil(t, parsePushContext(map[string]any{}))              // no after, no ref
		assert.Nil(t, parsePushContext(map[string]any{"after": "abc"})) // no branch
		assert.Nil(t, parsePushContext(map[string]any{"after": "abc", "ref": "main"})) // no prefix
		assert.Nil(t, parsePushContext(map[string]any{"after": "abc", "ref": "refs/heads/"})) // empty branch
		push := parsePushContext(map[string]any{"after": "abc", "ref": "refs/heads/dev"})
		require.NotNil(t, push); assert.Equal(t, "dev", push.Branch); assert.Equal(t, "abc", push.After)
	})

	t.Run("parseCheckRunContext: all shapes", func(t *testing.T) {
		cr, ok := parseCheckRunContext(map[string]any{})
		assert.False(t, ok); assert.Nil(t, cr)
		cr, ok = parseCheckRunContext(map[string]any{"check_run": map[string]any{"id": float64(99)}})
		require.True(t, ok); assert.Equal(t, int64(99), cr.ID)
		cr, ok = parseCheckRunContext(map[string]any{"check_run": map[string]any{"id": "55"}})
		require.True(t, ok); assert.Equal(t, int64(55), cr.ID)
		cr, ok = parseCheckRunContext(map[string]any{"check_run": map[string]any{"id": "notanum"}})
		require.True(t, ok); assert.Equal(t, int64(0), cr.ID)
		cr, ok = parseCheckRunContext(map[string]any{"check_run": map[string]any{"id": true}})
		require.True(t, ok); assert.Equal(t, int64(0), cr.ID)
		cr, ok = parseCheckRunContext(map[string]any{"check_run": map[string]any{"id": 1, "pull_requests": []any{map[string]any{}}}})
		require.True(t, ok); assert.Nil(t, cr.PR)
		payload := map[string]any{
			"check_run": map[string]any{"id": 1, "pull_requests": []any{map[string]any{"number": 3, "head": map[string]any{"sha": "shax"}}}},
			"requested_action": map[string]any{"identifier": "docsync/generate-patch"},
		}
		cr, ok = parseCheckRunContext(payload)
		require.True(t, ok)
		require.NotNil(t, cr.PR)
		assert.Equal(t, 3, cr.PR.Number)
		assert.Equal(t, "shax", cr.PR.HeadSha)
		require.NotNil(t, cr.RequestedActionIdentifier)
		assert.Equal(t, "docsync/generate-patch", *cr.RequestedActionIdentifier)
	})

	t.Run("parseWebhookBody: null and invalid", func(t *testing.T) {
		_, err := parseWebhookBody("null"); assert.Error(t, err)
		_, err = parseWebhookBody("not json"); assert.Error(t, err)
		parsed, err := parseWebhookBody(`{"a":1}`); require.NoError(t, err); assert.Equal(t, float64(1), parsed["a"])
	})

	t.Run("VerifyWebhookSignature: length-mismatch hex -> false", func(t *testing.T) {
		body := `{"action":"opened"}`
		assert.False(t, VerifyWebhookSignature(body, "sha256="+stringsRepeat("f", 32), "s"))
	})

	t.Run("errorsAsDuplicate: unwrap chain to nil -> false", func(t *testing.T) {
		var dup *DuplicateEscalationError
		assert.False(t, errorsAsDuplicate(nilUnwrap{}, &dup))
		assert.Nil(t, dup)
	})
	t.Run("errorsAsDuplicate: direct duplicate -> true", func(t *testing.T) {
		dup := &DuplicateEscalationError{IdempotencyKey: "k"}
		var target *DuplicateEscalationError
		assert.True(t, errorsAsDuplicate(dup, &target))
		assert.Equal(t, "k", target.IdempotencyKey)
	})
	t.Run("errorsAsDuplicate: no-unwrap error -> false", func(t *testing.T) {
		var target *DuplicateEscalationError
		assert.False(t, errorsAsDuplicate(errors.New("plain"), &target))
		assert.Nil(t, target)
	})

	t.Run("conclusionText: nil -> none", func(t *testing.T) {
		assert.Equal(t, "none", conclusionText(nil))
	})
	t.Run("githubAppError Error/Message", func(t *testing.T) {
		e := newGithubAppError("boom")
		assert.Equal(t, "boom", e.Error())
		assert.Equal(t, "boom", e.Message())
	})
}

type nilUnwrap struct{}

func (nilUnwrap) Error() string { return "nil unwrap" }
func (nilUnwrap) Unwrap() error { return nil }

func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func strPtrP(s string) *string { return &s }
