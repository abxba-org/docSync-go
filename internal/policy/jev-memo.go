// Ported from packages/policy/src/memo/jev-memo.ts (P2-T23; ARCH §7.1 item 4,
// D6) — identical (diff-subset, policy_version) decisions are memoized so Loop
// A short-circuits repeated Stage-1 calls.
//
// SHADOW-KEY ISOLATION (D6, binding): the cache key hashes the context
// namespace TOGETHER with the diff subset and the policy version, AND the
// storage is partitioned per namespace — two layers, so a shadow run can never
// read a production entry or poison one, in either direction. Namespaces are
// exactly 'production' and 'shadow:<binding>' (non-empty binding); anything
// else is a typed InvalidMemoNamespaceError (fail closed — a typo'd namespace
// that silently missed would hide isolation bugs).
//
// Key derivation (deterministic, no clock; P12-T03 — ARCH §3.5 idempotency,
// §7.1 item 4 + the PLAN §13.6 decision "shadow decision caches keyed by
// policy_version (+ model binding)"):
//
//	key = sha256( canonical(diffSubset) + '\0' + policyVersion + '\0' +
//	              contextNamespace + '\0' + modelBinding )
//
// where canonical(diffSubset) is MemoKeyDiffSubset: the sorted,
// canonical-JSON form of the deterministic change-analysis slice (file paths +
// statuses + sorted flags). The FULL ChangeAnalysis reduction into a diff
// subset is the ORCHESTRATOR's job (Phase 5) — this cache accepts a
// pre-reduced subset so it stays a pure key/value structure.
//
// MODEL-BINDING PINNING (P12-T03; D3): displayed confidence and the decision
// itself are pinned to (policy_version, model binding) — swapping the
// decision-engine binding invalidates memoized decisions exactly like a
// policy-version bump. The binding is the caller-supplied canonical D3
// binding identity (e.g. "model#provider"), treated opaquely here and salted
// into the key; it is INJECTABLE so tests can pin determinism. The member is
// OPTIONAL (nil == TS undefined) and an absent binding derives the SAME key
// bytes as before P12-T03 (additive: existing callers and pinned key shapes
// are unaffected); a PRESENT binding must be a non-empty string without NUL
// (fail closed — an ambiguous binding that silently collided would break the
// D3 pinning guarantee).
//
// In-memory V1 (files-only state, OD-3): no eviction, no TTL, no fs — Phase 5
// wires persistence/eviction. Stored decisions are schema-valid
// docsync.decision/v1 objects returned verbatim.
package policy

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DUDE616/docSync-go/internal/docutil"
	"github.com/DUDE616/docSync-go/internal/schemas"
)

// ProductionNamespace is the production namespace — the only non-shadow
// namespace (TS PRODUCTION_NAMESPACE).
const ProductionNamespace = "production"

// ShadowNamespacePrefix is the shadow namespace prefix: shadow namespaces are
// 'shadow:<binding>' with a non-empty binding (D6) (TS
// SHADOW_NAMESPACE_PREFIX).
const ShadowNamespacePrefix = "shadow:"

// IsJevMemoNamespace reports whether namespace is a valid memo namespace:
// production or shadow:<non-empty>.
func IsJevMemoNamespace(namespace string) bool {
	if namespace == ProductionNamespace {
		return true
	}
	if !strings.HasPrefix(namespace, ShadowNamespacePrefix) {
		return false
	}
	return len(namespace[len(ShadowNamespacePrefix):]) > 0
}

/* ------------------------------------------------------------------ errors */

// InvalidMemoNamespaceError is the fail-closed namespace violation (D6
// isolation vocabulary enforced at the API boundary; TS
// InvalidMemoNamespaceError). The TS class carried no machine-readable code,
// so it maps onto the docutil coded-error family directly.
type InvalidMemoNamespaceError struct {
	// Namespace is the rejected namespace.
	Namespace string
	// Message preserves the TS message text exactly.
	Message string
}

func newInvalidMemoNamespaceError(namespace string) *InvalidMemoNamespaceError {
	return &InvalidMemoNamespaceError{
		Namespace: namespace,
		Message: "invalid memo namespace " + strconv.Quote(namespace) +
			" — must be 'production' or 'shadow:<binding>' with a non-empty binding (D6 shadow-key isolation)",
	}
}

func (e *InvalidMemoNamespaceError) Error() string { return e.Message }

// Code returns the docutil code (a namespace violation is a validation
// failure at the API boundary).
func (e *InvalidMemoNamespaceError) Code() string { return docutil.CodeValidation }

// Unwrap exposes the docutil coded error so docutil.CodeOf/errors.As work
// uniformly across every docSync error.
func (e *InvalidMemoNamespaceError) Unwrap() error {
	return docutil.ValidationError("%s", e.Message)
}

// MemoDiffSubsetError is the fail-closed diff-subset shape violation
// (TS RangeError from assertDiffSubset).
type MemoDiffSubsetError struct {
	// Message preserves the TS message text exactly.
	Message string
}

func (e *MemoDiffSubsetError) Error() string { return e.Message }

// Code returns the docutil code (a malformed diff subset is a validation
// failure).
func (e *MemoDiffSubsetError) Code() string { return docutil.CodeValidation }

// Unwrap exposes the docutil coded error.
func (e *MemoDiffSubsetError) Unwrap() error {
	return docutil.ValidationError("%s", e.Message)
}

// MemoModelBindingError is the fail-closed model-binding violation
// (TS RangeError from assertModelBinding).
type MemoModelBindingError struct {
	// Message preserves the TS message text exactly.
	Message string
}

func (e *MemoModelBindingError) Error() string { return e.Message }

// Code returns the docutil code (a malformed binding identity is a validation
// failure).
func (e *MemoModelBindingError) Code() string { return docutil.CodeValidation }

// Unwrap exposes the docutil coded error.
func (e *MemoModelBindingError) Unwrap() error {
	return docutil.ValidationError("%s", e.Message)
}

/* ------------------------------------------------------------ diff subset */

// MemoDiffFile is one file of the deterministic diff subset — path + status +
// optional sorted flags (TS MemoDiffFile).
type MemoDiffFile struct {
	Path   string
	Status string
	// Flags are optional deterministic flags of the slice (sorted in the
	// canonical form); nil == TS undefined.
	Flags []string
}

// MemoDiffSubset is the pre-reduced change-analysis slice the cache keys on
// (orchestrator-reduced, Phase 5) (TS MemoDiffSubset).
type MemoDiffSubset struct {
	Files []MemoDiffFile
}

// assertDiffSubset is the TS assertDiffSubset: non-empty string paths,
// statuses and flags (RangeError in TS).
func assertDiffSubset(analysis MemoDiffSubset) error {
	for _, file := range analysis.Files {
		if file.Path == "" {
			return &MemoDiffSubsetError{
				Message: "memoKeyDiffSubset requires non-empty string file paths (got )",
			}
		}
		if file.Status == "" {
			return &MemoDiffSubsetError{
				Message: "memoKeyDiffSubset requires non-empty string file statuses (got )",
			}
		}
		for _, flag := range file.Flags {
			if flag == "" {
				return &MemoDiffSubsetError{
					Message: "memoKeyDiffSubset requires non-empty string flags (got )",
				}
			}
		}
	}
	return nil
}

// memoCanonicalFile is the canonical per-file form: flags sorted.
type memoCanonicalFile struct {
	Path   string
	Status string
	Flags  []string
}

// memoFileLess is the TS file comparator: (path, status, flags joined by
// \u0001) lexicographic order — byte-stable against input order.
func memoFileLess(a, b memoCanonicalFile) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Status != b.Status {
		return a.Status < b.Status
	}
	return strings.Join(a.Flags, "\u0001") < strings.Join(b.Flags, "\u0001")
}

// MemoKeyDiffSubset returns the canonical form of the diff subset: files
// sorted by (path, status, flags) — byte-stable against input order — with
// each file's flags sorted; serialized with the package-wide canonical JSON
// (docutil.CanonicalJSON). Deterministic: the same slice in any input order
// yields the same canonical string.
func MemoKeyDiffSubset(analysis MemoDiffSubset) (string, error) {
	if err := assertDiffSubset(analysis); err != nil {
		return "", err
	}
	canonical := make([]memoCanonicalFile, 0, len(analysis.Files))
	for _, file := range analysis.Files {
		flags := append([]string(nil), file.Flags...)
		sort.Strings(flags)
		canonical = append(canonical, memoCanonicalFile{Path: file.Path, Status: file.Status, Flags: flags})
	}
	sort.SliceStable(canonical, func(i, j int) bool { return memoFileLess(canonical[i], canonical[j]) })
	files := make([]any, 0, len(canonical))
	for _, file := range canonical {
		flags := make([]any, 0, len(file.Flags))
		for _, flag := range file.Flags {
			flags = append(flags, flag)
		}
		files = append(files, map[string]any{
			"path":   file.Path,
			"status": file.Status,
			"flags":  flags,
		})
	}
	serialized, err := docutil.CanonicalJSON(map[string]any{"files": files})
	if err != nil {
		return "", err
	}
	return serialized, nil
}

/* --------------------------------------------------------------- memo key */

// JevMemoKeyInput is the memo key derivation input (TS jevMemoKey input).
type JevMemoKeyInput struct {
	DiffSubset    MemoDiffSubset
	PolicyVersion int
	Namespace     string
	// ModelBinding is the optional D3 binding identity; nil == TS undefined
	// (the pre-P12-T03 key bytes). A non-nil empty string is INVALID.
	ModelBinding *string
}

// assertModelBinding is the TS assertModelBinding: when present the binding
// must be a non-empty string without NUL (RangeError in TS). Absent (nil) is
// valid: the key then derives exactly as before P12-T03 (additive backward
// compatibility).
func assertModelBinding(modelBinding *string) error {
	if modelBinding == nil {
		return nil
	}
	if *modelBinding == "" || strings.ContainsRune(*modelBinding, 0) {
		return &MemoModelBindingError{
			Message: "jevMemoKey modelBinding must be a non-empty string without NUL when provided (got " + *modelBinding + ")",
		}
	}
	return nil
}

// JevMemoKey is the memo cache key (P2-T23; P12-T03 — ARCH §3.5, §7.1 item 4,
// D3): sha256 over canonical(diffSubset) ⊕ NUL ⊕ policyVersion ⊕ NUL ⊕
// contextNamespace ⊕ NUL ⊕ modelBinding. The namespace is hashed INTO the key
// (first isolation layer) on top of the per-namespace storage partition
// (second layer) — D6 shadow isolation in both directions by construction.
// The model binding (PLAN §13.6: keyed by policy_version + model binding) is
// salted into the key when provided — the same diff-subset under a different
// decision-engine binding MISSES (D3 pinning) — and an absent binding
// preserves the pre-P12-T03 key bytes.
func JevMemoKey(input JevMemoKeyInput) (string, error) {
	if !IsJevMemoNamespace(input.Namespace) {
		return "", newInvalidMemoNamespaceError(input.Namespace)
	}
	if err := assertModelBinding(input.ModelBinding); err != nil {
		return "", err
	}
	canonical, err := MemoKeyDiffSubset(input.DiffSubset)
	if err != nil {
		return "", err
	}
	binding := ""
	if input.ModelBinding != nil {
		binding = *input.ModelBinding
	}
	prehash := strings.Join([]string{
		canonical,
		strconv.Itoa(input.PolicyVersion),
		input.Namespace,
		binding,
	}, "\x00")
	return docutil.ContentHash(prehash), nil
}

/* -------------------------------------------------------------- memo cache */

// JevMemoStats is the deterministic stats output (TS JevMemoStats).
type JevMemoStats struct {
	// Entries is the total memoized decisions across all namespaces.
	Entries int
	// ByNamespace holds per-namespace entry counts.
	ByNamespace map[string]int
}

// JevMemoCache is the in-memory Jev decision memo cache — deterministic,
// clock-free, no eviction (V1 in-memory; Phase 5 wires
// persistence/eviction). Isolation contract (D6): Get NEVER returns an entry
// set under a different namespace, and Set under a shadow namespace never
// overwrites a production entry.
//
// It is safe for concurrent use (shared mutable state guarded by a RWMutex;
// run tests with -race per CONVENTIONS.md).
type JevMemoCache struct {
	mu         sync.RWMutex
	namespaces map[string]map[string]*schemas.Decision
}

// NewJevMemoCache builds an empty cache.
func NewJevMemoCache() *JevMemoCache {
	return &JevMemoCache{namespaces: make(map[string]map[string]*schemas.Decision)}
}

// partitionLocked returns (creating if needed) the partition for namespace;
// the caller must hold the write lock. Mirrors the TS partition(): an invalid
// namespace is a typed error even when the partition would be created.
func (c *JevMemoCache) partitionLocked(namespace string) (map[string]*schemas.Decision, error) {
	if !IsJevMemoNamespace(namespace) {
		return nil, newInvalidMemoNamespaceError(namespace)
	}
	partition, ok := c.namespaces[namespace]
	if !ok {
		partition = make(map[string]*schemas.Decision)
		c.namespaces[namespace] = partition
	}
	return partition, nil
}

// KeyFor derives the cache key for (diff subset, policy version, namespace,
// optional model binding) — validates the namespace and, when present, the
// binding identity (P12-T03; D3 pinning). An absent binding derives the
// pre-P12-T03 key bytes.
func (c *JevMemoCache) KeyFor(input JevMemoKeyInput) (string, error) {
	return JevMemoKey(input)
}

// Set stores a decision under (namespace, key). Never touches other
// namespaces. The decision is stored verbatim (no re-validation), exactly as
// the TS implementation.
//
// Deviation note: a nil decision is stored like any other value and reads
// back as a miss (Go's zero-value map semantics) — the TS equivalent
// (`set(ns, k, undefined)`) is unobservable the same way.
func (c *JevMemoCache) Set(namespace, key string, decision *schemas.Decision) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	partition, err := c.partitionLocked(namespace)
	if err != nil {
		return err
	}
	partition[key] = decision
	return nil
}

// Get looks up (namespace, key). Same-namespace only — shadow/production are
// invisible to each other. A miss returns (nil, nil); an invalid namespace
// returns a typed *InvalidMemoNamespaceError.
func (c *JevMemoCache) Get(namespace, key string) (*schemas.Decision, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !IsJevMemoNamespace(namespace) {
		return nil, newInvalidMemoNamespaceError(namespace)
	}
	partition, ok := c.namespaces[namespace]
	if !ok {
		return nil, nil
	}
	return partition[key], nil
}

// Stats returns deterministic stats: totals + per-namespace counts.
//
// Deviation note: the TS method sorts the namespace keys of the returned
// record (JS object key order); a Go map has no key order, so the counts —
// the observable content — are returned unsorted (identical content).
func (c *JevMemoCache) Stats() JevMemoStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	byNamespace := make(map[string]int, len(c.namespaces))
	entries := 0
	for namespace, partition := range c.namespaces {
		byNamespace[namespace] = len(partition)
		entries += len(partition)
	}
	return JevMemoStats{Entries: entries, ByNamespace: byNamespace}
}

// ensure fmt stays referenced for future message parity extensions.
var _ = fmt.Sprintf
