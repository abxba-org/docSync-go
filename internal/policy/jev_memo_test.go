package policy_test

// Ported from packages/policy/test/jev-memo.test.ts (P2-T23; ARCH §7.1 item 4,
// D6) and the memo-cache parts of jev-memo-p12-t03-keying.test.ts (P12-T03;
// V-P12-7 — ARCH §3.5 idempotency, §7.1 item 4, D3, D6).
//
// The P12-T03 stage-seam assertions (createJevDecisionStage: shadow lane
// passthrough, per-binding cache hits, abstain-never-cached) exercise the
// orchestrator decision-stage module and are ported by that unit's tests; this
// file ports every assertion that exercises THIS module (key derivation,
// namespace isolation, the cache mechanics, the D3 model-binding keying).
//
// Golden key bytes are pinned from an independent re-implementation of the TS
// spec (memoKeyDiffSubset + jevMemoKey over sha256, computed with Node's
// createHash against the exact TS source algorithm).

import (
	"errors"
	"testing"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/policy"
	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jevDiffSubset is the TS DIFF_SUBSET fixture.
func jevDiffSubset() policy.MemoDiffSubset {
	return policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"symbols_changed"}},
			{Path: "src/payments/api.ts", Status: "modified"},
		},
	}
}

// The canonical D3 binding identities the tests inject (opaque to the cache) —
// the TS BINDING_A / BINDING_B fixtures.
const (
	jevBindingA = "anthropic/claude-sonnet-4.5#default"
	jevBindingB = "z-ai/glm-4.6#default"
)

func jevBindingPtr(binding string) *string { return &binding }

// jevMemoDecision is the TS aDecision fixture: a schema-valid
// docsync.decision/v1 decision (the TS builds it through emitDecision).
func jevMemoDecision(commit string) *schemas.Decision {
	return &schemas.Decision{
		Schema:              schemas.DecisionSchemaID,
		DecisionID:          "d_" + commit,
		PolicyVersion:       1,
		Change:              schemas.DecisionChange{Commit: commit, DiffRef: "git:81af92e", ChangeAnalysisRef: "analysis/01J9ZK6X8A"},
		ChangeKind:          schemas.ChangeKindBehavioral,
		DocumentationImpact: schemas.DocumentationImpactPossible,
		Risk:                schemas.RiskLevelMedium,
		Confidence:          0.91,
		Answers:             jevUniversalAnswers(),
		Candidates:          []string{"docs/payments.md"},
		Action:              schemas.ActionReview,
		Abstain:             false,
		AbstainReason:       nil,
		Evidence:            []schemas.Evidence{},
		Trace: schemas.DecisionTrace{
			Model:      "provider/routed",
			PolicyHash: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			LatencyMS:  120,
		},
	}
}

func TestJevMemoDecisionFixtureIsSchemaValid(t *testing.T) {
	// The TS aDecision round-trips emitDecision's fail-closed DECISION_SCHEMA
	// parse; pin the same validity for the Go literal.
	assert.Empty(t, jevMemoDecision("81af92e").Validate())
}

func TestJevMemoKeySensitivity(t *testing.T) {
	cache := policy.NewJevMemoCache()
	base, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)

	same, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	assert.Equal(t, base, same)

	oneFile, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset: policy.MemoDiffSubset{
			Files: []policy.MemoDiffFile{
				{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"symbols_changed"}},
			},
		},
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	assert.NotEqual(t, base, oneFile)

	otherVersion, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 2,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	assert.NotEqual(t, base, otherVersion)

	shadow, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "shadow:candidate-binding",
	})
	require.NoError(t, err)
	assert.NotEqual(t, base, shadow)
}

func TestMemoKeyDiffSubsetIsOrderAndFlagOrderInsensitive(t *testing.T) {
	canonical, err := policy.MemoKeyDiffSubset(jevDiffSubset())
	require.NoError(t, err)
	// Golden canonical bytes: keys sorted, files sorted by (path, status,
	// flags), each file's flags sorted, empty flags serialized as [].
	assert.Equal(t,
		`{"files":[{"flags":[],"path":"src/payments/api.ts","status":"modified"},{"flags":["symbols_changed"],"path":"src/payments/refund.ts","status":"modified"}]}`,
		canonical)

	shuffled, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/api.ts", Status: "modified"},
			{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"symbols_changed"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, canonical, shuffled)

	flagShuffled, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"b_flag", "a_flag"}},
		},
	})
	require.NoError(t, err)
	flagCanonical, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"a_flag", "b_flag"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, flagCanonical, flagShuffled)

	statusChanged, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/refund.ts", Status: "added", Flags: []string{"symbols_changed"}},
		},
	})
	require.NoError(t, err)
	assert.NotEqual(t, canonical, statusChanged)
}

func TestMemoKeyDiffSubsetMalformedSubsetsAreProgrammerErrors(t *testing.T) {
	_, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{{Path: "", Status: "modified"}},
	})
	require.Error(t, err)
	var subsetErr *policy.MemoDiffSubsetError
	require.True(t, errors.As(err, &subsetErr))
	assert.Equal(t, policy.MemoDiffSubsetError{
		Message: "memoKeyDiffSubset requires non-empty string file paths (got )",
	}, *subsetErr)

	_, err = policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{{Path: "src/a.ts", Status: ""}},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &subsetErr))
	assert.Equal(t, policy.MemoDiffSubsetError{
		Message: "memoKeyDiffSubset requires non-empty string file statuses (got )",
	}, *subsetErr)

	_, err = policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{{Path: "src/a.ts", Status: "modified", Flags: []string{""}}},
	})
	require.Error(t, err)
	require.True(t, errors.As(err, &subsetErr))
	assert.Equal(t, policy.MemoDiffSubsetError{
		Message: "memoKeyDiffSubset requires non-empty string flags (got )",
	}, *subsetErr)
}

func TestShadowKeyIsolationSameKeyStringUnderTwoNamespaces(t *testing.T) {
	cache := policy.NewJevMemoCache()
	sharedKey := "k_shared_across_namespaces"
	production := jevMemoDecision("81af92e")
	shadow := jevMemoDecision("shadow-commit")
	require.NoError(t, cache.Set(policy.ProductionNamespace, sharedKey, production))
	require.NoError(t, cache.Set("shadow:candidate-binding", sharedKey, shadow))

	gotProduction, err := cache.Get(policy.ProductionNamespace, sharedKey)
	require.NoError(t, err)
	require.Same(t, production, gotProduction)
	assert.NotSame(t, shadow, gotProduction)

	gotShadow, err := cache.Get("shadow:candidate-binding", sharedKey)
	require.NoError(t, err)
	require.Same(t, shadow, gotShadow)
	assert.NotSame(t, production, gotShadow)
}

func TestNamespaceSaltedKeysAreIsolatedInBothDirections(t *testing.T) {
	cache := policy.NewJevMemoCache()
	productionKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	shadowKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "shadow:candidate-binding",
	})
	require.NoError(t, err)
	assert.NotEqual(t, productionKey, shadowKey)

	production := jevMemoDecision("81af92e")
	require.NoError(t, cache.Set(policy.ProductionNamespace, productionKey, production))

	gotProduction, err := cache.Get(policy.ProductionNamespace, productionKey)
	require.NoError(t, err)
	require.Same(t, production, gotProduction)
	gotShadow, err := cache.Get("shadow:candidate-binding", productionKey)
	require.NoError(t, err)
	assert.Nil(t, gotShadow)

	require.NoError(t, cache.Set("shadow:candidate-binding", shadowKey, production))
	gotShadow, err = cache.Get("shadow:candidate-binding", shadowKey)
	require.NoError(t, err)
	require.Same(t, production, gotShadow)
	gotProduction, err = cache.Get(policy.ProductionNamespace, shadowKey)
	require.NoError(t, err)
	assert.Nil(t, gotProduction)
}

func TestTwoShadowBindingsAreIsolatedFromEachOther(t *testing.T) {
	cache := policy.NewJevMemoCache()
	keyA, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "shadow:binding-a",
	})
	require.NoError(t, err)
	keyB, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "shadow:binding-b",
	})
	require.NoError(t, err)
	require.NoError(t, cache.Set("shadow:binding-a", keyA, jevMemoDecision("81af92e")))

	gotA, err := cache.Get("shadow:binding-b", keyA)
	require.NoError(t, err)
	assert.Nil(t, gotA)
	gotB, err := cache.Get("shadow:binding-b", keyB)
	require.NoError(t, err)
	assert.Nil(t, gotB)
}

func TestInvalidMemoNamespacesAreRejectedOnKeyForSetAndGet(t *testing.T) {
	for _, namespace := range []string{"staging", "shadow:", "", "production ", "SHADOW:binding"} {
		t.Run(namespace, func(t *testing.T) {
			cache := policy.NewJevMemoCache()
			assert.False(t, policy.IsJevMemoNamespace(namespace))

			_, err := cache.KeyFor(policy.JevMemoKeyInput{
				DiffSubset:    jevDiffSubset(),
				PolicyVersion: 1,
				Namespace:     namespace,
			})
			require.Error(t, err)
			var nsErr *policy.InvalidMemoNamespaceError
			require.True(t, errors.As(err, &nsErr))
			assert.Equal(t, namespace, nsErr.Namespace)

			err = cache.Set(namespace, "k", jevMemoDecision("81af92e"))
			require.Error(t, err)
			require.True(t, errors.As(err, &nsErr))

			_, err = cache.Get(namespace, "k")
			require.Error(t, err)
			require.True(t, errors.As(err, &nsErr))
		})
	}
}

func TestValidMemoNamespaces(t *testing.T) {
	assert.True(t, policy.IsJevMemoNamespace(policy.ProductionNamespace))
	assert.True(t, policy.IsJevMemoNamespace("shadow:candidate-binding"))
	assert.True(t, policy.IsJevMemoNamespace("shadow:canary-v18-model-x"))
}

func TestMemoTypedErrorsCarryCodesAndExactMessages(t *testing.T) {
	// CONVENTIONS.md §Errors: the TS classes carry no machine-readable code
	// (InvalidMemoNamespaceError) or throw RangeError, so they map onto the
	// docutil coded-error family — discoverable via docutil.CodeOf.
	cache := policy.NewJevMemoCache()

	_, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "staging",
	})
	var nsErr *policy.InvalidMemoNamespaceError
	require.True(t, errors.As(err, &nsErr))
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(err))
	assert.Equal(t, docutil.CodeValidation, nsErr.Code())
	assert.Equal(t,
		`invalid memo namespace "staging" — must be 'production' or 'shadow:<binding>' with a non-empty binding (D6 shadow-key isolation)`,
		nsErr.Error())

	_, err = policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{{Path: "src/a.ts", Status: ""}},
	})
	var subsetErr *policy.MemoDiffSubsetError
	require.True(t, errors.As(err, &subsetErr))
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(subsetErr))
	assert.Equal(t, "memoKeyDiffSubset requires non-empty string file statuses (got )", subsetErr.Error())
	assert.Equal(t, docutil.CodeValidation, subsetErr.Code())

	// The malformed-subset boundary is enforced on the key path too.
	_, err = policy.JevMemoKey(policy.JevMemoKeyInput{
		DiffSubset:    policy.MemoDiffSubset{Files: []policy.MemoDiffFile{{Path: "", Status: "modified"}}},
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.True(t, errors.As(err, &subsetErr))

	_, err = policy.JevMemoKey(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
		ModelBinding:  jevBindingPtr(""),
	})
	var bindingErr *policy.MemoModelBindingError
	require.True(t, errors.As(err, &bindingErr))
	assert.Equal(t, docutil.CodeValidation, docutil.CodeOf(bindingErr))
	assert.Equal(t, docutil.CodeValidation, bindingErr.Code())
	assert.Equal(t,
		"jevMemoKey modelBinding must be a non-empty string without NUL when provided (got )",
		bindingErr.Error())

	_, err = policy.JevMemoKey(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
		ModelBinding:  jevBindingPtr("model#default\x00x"),
	})
	require.True(t, errors.As(err, &bindingErr))
	assert.Equal(t,
		"jevMemoKey modelBinding must be a non-empty string without NUL when provided (got model#default\x00x)",
		bindingErr.Error())
}

func TestMemoKeyDiffSubsetComparatorTieBreaks(t *testing.T) {
	// Path ties break on status; path+status ties break on the sorted flags —
	// both regardless of input order.
	statusTieA, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/a.ts", Status: "modified", Flags: []string{"x"}},
			{Path: "src/a.ts", Status: "added"},
		},
	})
	require.NoError(t, err)
	statusTieB, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/a.ts", Status: "added"},
			{Path: "src/a.ts", Status: "modified", Flags: []string{"x"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, statusTieB, statusTieA)

	flagsTieA, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/a.ts", Status: "modified", Flags: []string{"x"}},
			{Path: "src/a.ts", Status: "modified", Flags: []string{"y"}},
		},
	})
	require.NoError(t, err)
	flagsTieB, err := policy.MemoKeyDiffSubset(policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/a.ts", Status: "modified", Flags: []string{"y"}},
			{Path: "src/a.ts", Status: "modified", Flags: []string{"x"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, flagsTieB, flagsTieA)
}

func TestCacheGetMissThenSetRoundTripsTheSameDecision(t *testing.T) {
	cache := policy.NewJevMemoCache()
	key, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)

	miss, err := cache.Get(policy.ProductionNamespace, key)
	require.NoError(t, err)
	assert.Nil(t, miss)

	decision := jevMemoDecision("81af92e")
	require.NoError(t, cache.Set(policy.ProductionNamespace, key, decision))
	got, err := cache.Get(policy.ProductionNamespace, key)
	require.NoError(t, err)
	require.Same(t, decision, got)
}

func TestCacheStatsTotalsAndPerNamespaceCounts(t *testing.T) {
	cache := policy.NewJevMemoCache()
	productionKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	shadowKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     "shadow:candidate-binding",
	})
	require.NoError(t, err)

	require.NoError(t, cache.Set("shadow:candidate-binding", shadowKey, jevMemoDecision("shadow-commit")))
	assert.Equal(t, policy.JevMemoStats{
		Entries:     1,
		ByNamespace: map[string]int{"shadow:candidate-binding": 1},
	}, cache.Stats())

	require.NoError(t, cache.Set(policy.ProductionNamespace, productionKey, jevMemoDecision("81af92e")))
	emptySubsetKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    policy.MemoDiffSubset{Files: []policy.MemoDiffFile{}},
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	require.NoError(t, cache.Set(policy.ProductionNamespace, emptySubsetKey, jevMemoDecision("82af92e")))

	assert.Equal(t, policy.JevMemoStats{
		Entries:     3,
		ByNamespace: map[string]int{"production": 2, "shadow:candidate-binding": 1},
	}, cache.Stats())
}

func TestSetUnderAShadowNamespaceNeverOverwritesAProductionEntry(t *testing.T) {
	cache := policy.NewJevMemoCache()
	key, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	production := jevMemoDecision("81af92e")
	require.NoError(t, cache.Set(policy.ProductionNamespace, key, production))
	require.NoError(t, cache.Set("shadow:candidate-binding", key, jevMemoDecision("shadow-commit")))

	got, err := cache.Get(policy.ProductionNamespace, key)
	require.NoError(t, err)
	require.Same(t, production, got)
}

func TestJevMemoKeyDerivationIsDeterministicAndCanonical(t *testing.T) {
	cache := policy.NewJevMemoCache()
	keyArgs := policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
		ModelBinding:  jevBindingPtr(jevBindingA),
	}
	base, err := cache.KeyFor(keyArgs)
	require.NoError(t, err)

	// Identical logical inputs → identical key bytes (repeat derivation).
	again, err := cache.KeyFor(keyArgs)
	require.NoError(t, err)
	assert.Equal(t, base, again)

	// Canonical diff-subset form: file order and flag order are irrelevant.
	shuffledArgs := keyArgs
	shuffledArgs.DiffSubset = policy.MemoDiffSubset{
		Files: []policy.MemoDiffFile{
			{Path: "src/payments/api.ts", Status: "modified"},
			{Path: "src/payments/refund.ts", Status: "modified", Flags: []string{"symbols_changed"}},
		},
	}
	shuffledKey, err := cache.KeyFor(shuffledArgs)
	require.NoError(t, err)
	assert.Equal(t, base, shuffledKey)

	// Any LOGICAL input change (subset, version, binding, lane) changes the
	// key — the binding is not swallowed by the join.
	otherVersion := keyArgs
	otherVersion.PolicyVersion = 2
	otherVersionKey, err := cache.KeyFor(otherVersion)
	require.NoError(t, err)
	assert.NotEqual(t, base, otherVersionKey)

	otherBinding := keyArgs
	otherBinding.ModelBinding = jevBindingPtr(jevBindingB)
	otherBindingKey, err := cache.KeyFor(otherBinding)
	require.NoError(t, err)
	assert.NotEqual(t, base, otherBindingKey)

	otherNamespace := keyArgs
	otherNamespace.Namespace = "shadow:candidate-binding"
	otherNamespaceKey, err := cache.KeyFor(otherNamespace)
	require.NoError(t, err)
	assert.NotEqual(t, base, otherNamespaceKey)

	// Fail closed on an ambiguous binding identity: empty or NUL-bearing
	// bindings are programmer errors, never silently-colliding keys.
	emptyBinding := keyArgs
	emptyBinding.ModelBinding = jevBindingPtr("")
	_, err = cache.KeyFor(emptyBinding)
	require.Error(t, err)
	var bindingErr *policy.MemoModelBindingError
	require.True(t, errors.As(err, &bindingErr))

	nulBinding := keyArgs
	nulBinding.ModelBinding = jevBindingPtr("model#default\x00x")
	_, err = cache.KeyFor(nulBinding)
	require.Error(t, err)
	require.True(t, errors.As(err, &bindingErr))

	// The namespace vocabulary keeps its own fail-closed boundary.
	badNamespace := keyArgs
	badNamespace.Namespace = "staging"
	_, err = cache.KeyFor(badNamespace)
	require.Error(t, err)
	var nsErr *policy.InvalidMemoNamespaceError
	require.True(t, errors.As(err, &nsErr))
}

func TestJevMemoKeyModelBindingIsSaltedIntoTheKey(t *testing.T) {
	cache := policy.NewJevMemoCache()
	withoutBinding, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	withBinding, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    jevDiffSubset(),
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
		ModelBinding:  jevBindingPtr(jevBindingA),
	})
	require.NoError(t, err)
	assert.NotEqual(t, withoutBinding, withBinding)
}

// TestJevMemoKeyGoldenBytes pins the exact sha256 key derivations against an
// independent re-implementation of the TS algorithm (canonical diff-subset
// JSON + '\0'-joined 4-field prehash).
func TestJevMemoKeyGoldenBytes(t *testing.T) {
	cache := policy.NewJevMemoCache()
	keyFor := func(namespace string, policyVersion int, binding *string) string {
		key, err := cache.KeyFor(policy.JevMemoKeyInput{
			DiffSubset:    jevDiffSubset(),
			PolicyVersion: policyVersion,
			Namespace:     namespace,
			ModelBinding:  binding,
		})
		require.NoError(t, err)
		return key
	}

	assert.Equal(t,
		"aef51f0615d48a1fa99af366276efc285967a9d84c29e524bcfc1f64b2d5321f",
		keyFor(policy.ProductionNamespace, 1, nil))
	assert.Equal(t,
		"c3eafe2b7061edd3cc3b09b8afda6d46a45c5c4fe4e1ed072bbf99595f2c5f21",
		keyFor(policy.ProductionNamespace, 1, jevBindingPtr(jevBindingA)))
	assert.Equal(t,
		"7098b498d313ba9724a96af9abca4d6c57b3edc749616981f62a69c5f4cbbebf",
		keyFor(policy.ProductionNamespace, 1, jevBindingPtr(jevBindingB)))
	assert.Equal(t,
		"c0d412a1eb0e930641c8d0c309696d010a3bd676aa72537d593b0e59b5603708",
		keyFor(policy.ProductionNamespace, 2, jevBindingPtr(jevBindingA)))
	assert.Equal(t,
		"571653a68b899a4ade3340c45965d71e8ca2be6cb21f54ec9cb5637ad984960a",
		keyFor("shadow:candidate-binding", 1, jevBindingPtr(jevBindingA)))

	emptySubsetKey, err := cache.KeyFor(policy.JevMemoKeyInput{
		DiffSubset:    policy.MemoDiffSubset{Files: []policy.MemoDiffFile{}},
		PolicyVersion: 1,
		Namespace:     policy.ProductionNamespace,
	})
	require.NoError(t, err)
	assert.Equal(t,
		"36fb80d5965915b2fbf38c0121536378fc79d042081632f5262b7b7b3967eeff",
		emptySubsetKey)
}
