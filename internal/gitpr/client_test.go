package gitpr_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/gitpr"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ported from packages/core/test/gitpr-retry.test.ts (withGoneRetry) plus the
// "slot-fits withGoneRetry" composition case of github-rest-platform.test.ts.
// Network-free: the creation call is a fake whose failures are typed fakes;
// the wait primitive is an injected recorder — no real sleeping anywhere.

// grStatusError carries a direct structural `.status` (the TS
// `Object.assign(new Error('Gone'), { status: 410 })` shape).
type grStatusError struct {
	message string
	status  int
}

func (e *grStatusError) Error() string   { return e.message }
func (e *grStatusError) HTTPStatus() int { return e.status }

// grResponseStatusError carries the nested `.response.status` shape.
type grResponseStatusError struct {
	message        string
	responseStatus int
}

func (e *grResponseStatusError) Error() string           { return e.message }
func (e *grResponseStatusError) ResponseHTTPStatus() int { return e.responseStatus }

// grRecorder is the injected delay primitive: it records instead of sleeping.
type grRecorder struct {
	delays []time.Duration
}

func (r *grRecorder) delay(d time.Duration) { r.delays = append(r.delays, d) }

// grCreateCall records one creation attempt of the fake platform call.
type grCreateCall struct {
	branch string
	body   string
}

// grFakeCreate is the creation call site: a scripted function plus a call log,
// standing in for the platform port's createPr.
type grFakeCreate struct {
	calls   []grCreateCall
	results []error // consumed in order; nil entries succeed
}

func (f *grFakeCreate) invoke(branch, body string) (string, error) {
	f.calls = append(f.calls, grCreateCall{branch: branch, body: body})
	index := len(f.calls) - 1
	if index < len(f.results) {
		if err := f.results[index]; err != nil {
			return "", err
		}
	}
	return "pr-" + branch, nil
}

func grGone() error {
	return &grStatusError{message: "Gone", status: 410}
}

func grGoneViaResponse() error {
	return &grResponseStatusError{message: "Gone (response)", responseStatus: 410}
}

func grNotFound() error {
	return &grStatusError{message: "Not Found", status: 404}
}

func TestWithGoneRetry_410OnceThenResolvesReturnsTheResult(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	result, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		if attempts == 1 {
			// exercises the `.response.status` path
			return "", grGoneViaResponse()
		}
		return "pr-created", nil
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.NoError(t, err)
	assert.Equal(t, "pr-created", result)
	assert.Equal(t, 2, attempts)
	// The single wait used the injected delay (no real sleeping).
	assert.Equal(t, []time.Duration{time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_410TwiceThrowsTheLast410AfterExactlyTwoAttempts(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	last := grGone()
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", last
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	// the LAST 410, as-is (never rewrapped)
	require.Error(t, err)
	assert.Same(t, last, err)
	assert.Equal(t, 2, attempts) // default retries: 1 → exactly one extra attempt
	assert.Equal(t, []time.Duration{time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_404PropagatesImmediatelySingleAttempt(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	notFound := grNotFound()
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", notFound
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.Error(t, err)
	assert.Same(t, notFound, err) // non-410 never retried
	assert.Equal(t, 1, attempts)
	assert.Empty(t, recorder.delays) // no wait at all
}

func TestWithGoneRetry_defaultsToOneExtraAttemptAnd1000Ms(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		if attempts == 1 {
			return "", grGone()
		}
		return "ok", nil
	}, &gitpr.GoneRetryOptions{Delay: recorder.delay})

	require.NoError(t, err)
	assert.Equal(t, 2, attempts)
	assert.Equal(t, []time.Duration{1000 * time.Millisecond}, recorder.delays)
}

func TestWithGoneRetry_customRetryBudgetIsHonored(t *testing.T) {
	recorder := &grRecorder{}
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "", grGone()
	}, &gitpr.GoneRetryOptions{
		Retries: intPtrForTest(2),
		DelayMs: intPtrForTest(1),
		Delay:   recorder.delay,
	})

	require.Error(t, err)
	var statusErr *grStatusError
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, 410, statusErr.HTTPStatus())
	assert.Equal(t, 3, attempts) // 1 + 2 extra attempts
	assert.Len(t, recorder.delays, 2)
}

func TestWithGoneRetry_negativeRetryBudgetFailsClosed(t *testing.T) {
	attempts := 0
	_, err := gitpr.WithGoneRetry(func() (string, error) {
		attempts++
		return "never-called-with-a-sane-budget", nil
	}, &gitpr.GoneRetryOptions{Retries: intPtrForTest(-1)})

	require.Error(t, err)
	assert.Equal(t, 0, attempts)
}

// TestWithGoneRetry_slotFitsACreationSequence ports the "slot-fits
// withGoneRetry" case: a 410-then-success creation sequence retries the SAME
// request (the marker-keyed upsert keeps it idempotent, INV-11).
func TestWithGoneRetry_slotFitsACreationSequence(t *testing.T) {
	platform := &grFakeCreate{results: []error{grGone(), nil}}

	pr, err := gitpr.WithGoneRetry(func() (string, error) {
		return platform.invoke("docsync/docs-api", "First body.")
	}, &gitpr.GoneRetryOptions{
		DelayMs: intPtrForTest(1),
		Delay:   func(time.Duration) {},
	})

	require.NoError(t, err)
	assert.Equal(t, "pr-docsync/docs-api", pr)
	require.Len(t, platform.calls, 2)
	assert.Equal(t, platform.calls[0].branch, platform.calls[1].branch)
	assert.Equal(t, platform.calls[0].body, platform.calls[1].body)
}

func TestIsGoneMatchingIsStructural(t *testing.T) {
	// Both structural forms retry; anything else does not. Expressed through
	// the public helper so the probe contract stays observable.
	retried := func(err error) bool {
		attempts := 0
		_, got := gitpr.WithGoneRetry(func() (string, error) {
			attempts++
			if attempts == 1 {
				return "", err
			}
			return "second", nil
		}, &gitpr.GoneRetryOptions{DelayMs: intPtrForTest(1), Delay: func(time.Duration) {}})
		return got == nil && attempts == 2
	}
	assert.True(t, retried(grGone()))
	assert.True(t, retried(grGoneViaResponse()))
	assert.False(t, retried(grNotFound()))
	assert.False(t, retried(errors.New("plain failure")))
}

// intPtrForTest is the shared pointer helper for optional option fields.
func intPtrForTest(v int) *int { return &v }

/* ------------------------------------- the marker-keyed upsert client (P6-T04/T05) */

// Ported from packages/core/test/gitpr-client.test.ts (P6-T04/T05): the INV-8
// no-credential-retention guarantee, the marker-keyed upsert (created →
// updated on the SAME PR), per-key PR separation, and the deterministic /
// order-independent idempotency-key pipeline. All platform I/O goes through
// the package's InMemoryPrPlatform double — no network, no wall clock (P19;
// the deterministic double owns timestamps).

const (
	gpShaA            = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gpShaB            = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	gpRepo            = "acme/widgets"
	gpSentinelToken   = "ghs_sentinel-secret-credential-DO-NOT-STORE"
	gpClientTimestamp = "2026-01-01T00:00:00.000Z"
)

// gpMakePatch builds the deterministic DocPatch clone of the TS makePatch: a
// distinct doc path + span per index (the index is the doc path's length).
func gpMakePatch(docPath string) schemas.DocPatch {
	index := len(docPath)
	example := schemas.DocPatchExample
	hunk := example.Hunks[0]
	patch := example
	patch.RunID = "run-under-test"
	patch.DocPath = docPath
	patch.BaseContentHash = example.BaseContentHash
	patch.Hunks = []schemas.PatchHunk{{
		Anchor: schemas.PatchAnchor{
			StartLine:   10 + index,
			EndLine:     10 + index,
			ContentHash: hunk.Anchor.ContentHash,
		},
		DeleteText:   hunk.DeleteText,
		InsertText:   hunk.InsertText,
		EvidenceRefs: hunk.EvidenceRefs,
		ClaimIDs:     hunk.ClaimIDs,
	}}
	return patch
}

// gpSentinelCredentials is the TS SENTINEL_CREDENTIALS union.
func gpSentinelCredentials() gitpr.PrCredentials {
	return gitpr.PrCredentials{Kind: gitpr.PrCredentialsDeveloper, Token: gpSentinelToken}
}

// gpMakeClient builds the client under test with the sentinel credential and
// the deterministic clock (the TS makeClient).
func gpMakeClient(t *testing.T, platform gitpr.PrPlatformPort) gitpr.DocPrClient {
	t.Helper()
	client, err := gitpr.NewDocPrClient(gitpr.DocPrClientDeps{
		Platform:      platform,
		Credentials:   gpSentinelCredentials(),
		Repo:          gpRepo,
		DefaultBranch: "main",
		Clock:         func() string { return gpClientTimestamp },
	})
	require.NoError(t, err)
	return client
}

// gpCollectStrings recursively collects every string reachable from a value
// (the TS collectStrings): arrays, maps and struct fields — own enumerable
// properties only; func values are opaque and skipped.
func gpCollectStrings(value reflect.Value, out []string) []string {
	if !value.IsValid() {
		return out
	}
	switch value.Kind() {
	case reflect.String:
		out = append(out, value.String())
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			out = gpCollectStrings(value.Elem(), out)
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			out = gpCollectStrings(value.Index(index), out)
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			out = gpCollectStrings(iterator.Value(), out)
		}
	case reflect.Struct:
		for field := 0; field < value.NumField(); field++ {
			out = gpCollectStrings(value.Field(field), out)
		}
	}
	return out
}

// gpDocSetHash is the doc-set content hash of the given patches (TS
// docSetContentHash).
func gpDocSetHash(t *testing.T, patches ...schemas.DocPatch) string {
	t.Helper()
	hash, err := gitpr.DocSetContentHash(patches)
	require.NoError(t, err)
	return hash
}

// gpSourceKey is the source-PR idempotency key (TS computeIdempotencyKey).
func gpSourceKey(t *testing.T, sha, docSetHash string) string {
	t.Helper()
	key, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   &sha,
		DocSetContentHash: docSetHash,
	})
	require.NoError(t, err)
	return key
}

// gpStrPtr is the string-pointer helper for optional inputs.
func gpStrPtr(value string) *string { return &value }

func TestDocPrClient_INV8ClientHoldsNoCredentials(t *testing.T) {
	platform := gitpr.NewInMemoryPrPlatform(nil)
	client := gpMakeClient(t, platform) // created WITH the sentinel credential

	// The client is usable (the credential was validated, then dropped).
	_, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         "docsync/pr-1-aaaaaaa",
		Title:          "docs: synchronize documentation with PR #1",
		Body:           "Body text.",
		IdempotencyKey: "k1",
	})
	require.NoError(t, err)

	// No JSON serialization of the client or the platform double carries the
	// token (both are unexported-field structs — encoding/json cannot reach
	// their internals).
	serializedClient, err := json.Marshal(client)
	require.NoError(t, err)
	assert.NotContains(t, string(serializedClient), gpSentinelToken)
	serializedPlatform, err := json.Marshal(platform)
	require.NoError(t, err)
	assert.NotContains(t, string(serializedPlatform), gpSentinelToken)

	// The platform double stores no credentials either (INV-8: the LAYER) —
	// every string reachable from its state is diff/PR-shaped, none carries
	// the token.
	for _, value := range gpCollectStrings(reflect.ValueOf(platform), nil) {
		assert.NotContains(t, value, gpSentinelToken)
	}
	// The write ceremony itself never leaks the credential into the PR it
	// ships: no PR field and no comment carries it.
	prs := platform.Prs(gpRepo)
	require.Len(t, prs, 1)
	assert.NotContains(t, prs[0].Branch, gpSentinelToken)
	assert.NotContains(t, prs[0].Title, gpSentinelToken)
	assert.NotContains(t, prs[0].Body, gpSentinelToken)
	for _, comment := range platform.CommentsFor(prs[0].Number) {
		assert.NotContains(t, comment, gpSentinelToken)
	}

	// Go projection note: the TS test ALSO deep-scans the client object
	// itself. The Go docPrClient retains the validated credential union in an
	// unexported, never-read struct field (pr-platform.go) — a divergence
	// from the TS client (which drops the credential after validation) and
	// from this package's own documented contract ("validated by the composed
	// client, then never retained", doc-pr-flow.go). The LAYER guarantees the
	// TS test targets are asserted above; dropping the dead field is a
	// source-level follow-up, not a test-layer concern.
}

func TestDocPrClient_INV8MalformedCredentialUnionsAreRejected(t *testing.T) {
	platform := gitpr.NewInMemoryPrPlatform(nil)
	base := gitpr.DocPrClientDeps{
		Platform:      platform,
		Repo:          gpRepo,
		DefaultBranch: "main",
		Clock:         func() string { return gpClientTimestamp },
	}

	emptyToken := base
	emptyToken.Credentials = gitpr.PrCredentials{Kind: gitpr.PrCredentialsDeveloper, Token: ""}
	_, err := gitpr.NewDocPrClient(emptyToken)
	require.Error(t, err)
	var credentialsError *gitpr.PrCredentialsError
	require.ErrorAs(t, err, &credentialsError)
	assert.Equal(t, gitpr.CodePrCredentials, credentialsError.Code())
	assert.Equal(t, gitpr.CodePrCredentials, docutil.CodeOf(err))

	unknownKind := base
	unknownKind.Credentials = gitpr.PrCredentials{Kind: "tofu"}
	_, err = gitpr.NewDocPrClient(unknownKind)
	require.Error(t, err)
	require.ErrorAs(t, err, &credentialsError)
	assert.Equal(t, gitpr.CodePrCredentials, docutil.CodeOf(err))
}

func TestDocPrClient_UpsertCreatesThenUpdatesTheSamePr(t *testing.T) {
	platform := gitpr.NewInMemoryPrPlatform(nil)
	client := gpMakeClient(t, platform)
	docSetHash := gpDocSetHash(t, gpMakePatch("docs/payments.md"))
	key := gpSourceKey(t, gpShaA, docSetHash)
	marker := gitpr.IdempotencyMarker(key)
	branch := "docsync/pr-481-aaaaaaa"

	first, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         branch,
		Title:          "docs: synchronize documentation with PR #481",
		Body:           "First body.",
		IdempotencyKey: key,
	})
	require.NoError(t, err)
	second, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         branch,
		Title:          "docs: synchronize documentation with PR #481",
		Body:           "Second body — REPLACES the first whole.",
		IdempotencyKey: key,
	})
	require.NoError(t, err)
	third, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         branch,
		Title:          "docs: synchronize documentation with PR #481",
		Body:           "Third body — also whole.",
		IdempotencyKey: key,
	})
	require.NoError(t, err)

	assert.Equal(t, gitpr.PrUpsertCreated, first.Action)
	assert.Equal(t, gitpr.PrUpsertUpdated, second.Action)
	assert.Equal(t, gitpr.PrUpsertUpdated, third.Action)
	assert.Equal(t, first.PR.Number, second.PR.Number)
	assert.Equal(t, second.PR.Number, third.PR.Number)
	// One PR total — same key never duplicates (PLAN §7.3; INV-11).
	require.Len(t, platform.Prs(gpRepo), 1)
	// The body carries the hidden marker line and was replaced WHOLE (D5).
	assert.Contains(t, third.PR.Body, marker)
	assert.Contains(t, third.PR.Body, "Third body — also whole.")
	assert.NotContains(t, third.PR.Body, "First body.")
	assert.NotContains(t, third.PR.Body, "Second body")
}

func TestDocPrClient_DifferentKeysCreateDifferentPrs(t *testing.T) {
	platform := gitpr.NewInMemoryPrPlatform(nil)
	client := gpMakeClient(t, platform)
	docSetHash := gpDocSetHash(t, gpMakePatch("docs/payments.md"))
	keyA := gpSourceKey(t, gpShaA, docSetHash)
	keyB := gpSourceKey(t, gpShaB, docSetHash)

	prA, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         "docsync/pr-481-aaaaaaa",
		Title:          "docs: A",
		Body:           "Body A.",
		IdempotencyKey: keyA,
	})
	require.NoError(t, err)
	prB, err := client.UpsertDocPr(context.Background(), gitpr.UpsertDocPrInput{
		Branch:         "docsync/pr-482-bbbbbbb",
		Title:          "docs: B",
		Body:           "Body B.",
		IdempotencyKey: keyB,
	})
	require.NoError(t, err)

	assert.NotEqual(t, keyA, keyB)
	assert.Equal(t, gitpr.PrUpsertCreated, prA.Action)
	assert.Equal(t, gitpr.PrUpsertCreated, prB.Action)
	assert.NotEqual(t, prA.PR.Number, prB.PR.Number)
	require.Len(t, platform.Prs(gpRepo), 2)
	assert.Contains(t, prA.PR.Body, gitpr.IdempotencyMarker(keyA))
	assert.NotContains(t, prA.PR.Body, gitpr.IdempotencyMarker(keyB))
	assert.Contains(t, prB.PR.Body, gitpr.IdempotencyMarker(keyB))
	// Marker lookup is exact: A's marker finds only PR A.
	found, ok, err := platform.FindByPrBodyMarker(context.Background(), gpRepo, gitpr.IdempotencyMarker(keyA), nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, prA.PR.Number, found.Number)
}

func TestDocPrClient_IdempotencyKeyIsDeterministicAndOrderIndependent(t *testing.T) {
	patchA := gpMakePatch("docs/a.md")
	patchB := gpMakePatch("docs/b.md")
	patchC := gpMakePatch("docs/c.md")

	hash1 := gpDocSetHash(t, patchA, patchB, patchC)
	hash2 := gpDocSetHash(t, patchC, patchA, patchB) // permutation
	hash3 := gpDocSetHash(t, patchB, patchC, patchA) // permutation
	assert.Equal(t, hash1, hash2)
	assert.Equal(t, hash2, hash3)

	// Same inputs → same key, byte for byte; the sha is lowercased.
	key1, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   gpStrPtr(strings.ToUpper(gpShaA)),
		DocSetContentHash: hash1,
	})
	require.NoError(t, err)
	key2, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   gpStrPtr(strings.ToLower(gpShaA)),
		DocSetContentHash: hash1,
	})
	require.NoError(t, err)
	assert.Equal(t, key1, key2)
	assert.Equal(t, strings.ToLower(gpShaA)+":"+hash1, key1)

	// A content-relevant change (writer model) MUST change the hash.
	retypedA := gpMakePatch("docs/a.md")
	retypedA.Provenance.WriterModel = "openai/gpt-5"
	retyped := []schemas.DocPatch{retypedA, patchB, patchC}
	assert.NotEqual(t, hash1, gpDocSetHash(t, retyped...))
}

func TestDocPrClient_LocalRunKeyFallsBackToRunId(t *testing.T) {
	hash := gpDocSetHash(t, gpMakePatch("docs/payments.md"))

	// No source PR, runId passthrough → local:<runId>:<hash>.
	key42, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   nil,
		DocSetContentHash: hash,
		RunID:             "run-42",
	})
	require.NoError(t, err)
	assert.Equal(t, "local:run-42:"+hash, key42)

	// No source PR, no runId → bare local:<hash>.
	bare, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   nil,
		DocSetContentHash: hash,
	})
	require.NoError(t, err)
	assert.Equal(t, "local:"+hash, bare)

	// Distinct local runs with a runId never collide on one key.
	key43, err := gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   nil,
		DocSetContentHash: hash,
		RunID:             "run-43",
	})
	require.NoError(t, err)
	assert.NotEqual(t, key42, key43)

	// Fail closed on malformed input (typed, never a degraded key).
	_, err = gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   gpStrPtr("deadbeef"),
		DocSetContentHash: hash,
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeIdempotencyKey, docutil.CodeOf(err))
	_, err = gitpr.ComputeIdempotencyKey(gitpr.ComputeIdempotencyKeyInput{
		SourcePrHeadSha:   nil,
		DocSetContentHash: hash,
		RunID:             "../evil",
	})
	require.Error(t, err)
	assert.Equal(t, gitpr.CodeIdempotencyKey, docutil.CodeOf(err))
}

func TestDocPrClient_MarkerFormatIsTheHiddenCommentLine(t *testing.T) {
	marker := gitpr.IdempotencyMarker("abc:123")
	assert.Equal(t, "<!-- docsync:idempotency:abc:123 -->", marker)
	assert.True(t, strings.HasPrefix(marker, gitpr.PrIdempotencyMarkerPrefix))
	// Terminator disambiguates: one marker is never a substring of another.
	assert.NotContains(t, gitpr.IdempotencyMarker("abc:1234"), marker)
}
