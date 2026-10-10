// Ported from packages/analyzer-typescript/src/proxy-measure.ts (P11-T22; ARCH
// §16.1 A5; D14) — the "stats"/measurement surface of this package: it measures
// WHICH deterministic behavioral proxies fire for a before/after TypeScript
// source pair, the narrow-port input the A5 audit consumes
// (coverageaudit.BehavioralProxyCoverageCaseInput).
//
// Proxy mapping (measured, not guessed — the same evidence the analyzer reports
// per file):
//   - signature   — any paired symbol changed its signature text
//     (SymbolChangeKind 'signature').
//   - type        — any paired symbol changed its written type annotations
//     (SymbolChangeKind 'type').
//   - visibility  — any paired symbol flipped its exported flag
//     (SymbolChangeKind 'visibility').
//   - api-surface — any EXPORTED symbol was added or removed (public surface
//     changed — a deleted exported symbol is as much an API-surface change as
//     an added one, mirroring the analyzer's api_diff rule).
//
// A change to a function BODY ONLY (the classic pure-behavioral change) fires
// NOTHING — that is exactly the recall gap the A5 audit measures, declared per
// language in the report, never silent (D14).
//
// PURE (plugin-contract purity): no fs/network/git/clock; deterministic for
// identical input — same sources yield a deep-equal measurement. The empty
// string means "side absent" (added/deleted file), matching
// BeforeTextFromPatch/AfterTextFromPatch reconstruction semantics.

package tstool

import "sort"

// BehavioralProxyMeasurement is the measured record for one before/after source
// pair (port of BehavioralProxyMeasurement).
type BehavioralProxyMeasurement struct {
	// ProxyFired is true when at least one behavioral proxy fired.
	ProxyFired bool `json:"proxyFired"`
	// FiredProxies are the fired proxy kinds (a sorted subset of the audit's
	// proxy vocabulary: signature / type / visibility / api-surface).
	FiredProxies []string `json:"firedProxies"`
	// Diff is the full symbol diff — the evidence behind the measurement.
	Diff SymbolDiff `json:"diff"`
}

// BehavioralProxyMeasurementNote is the stable description of the measurement
// scope (declared, not silent). Verbatim
// BEHAVIORAL_PROXY_MEASUREMENT_NOTE.
const BehavioralProxyMeasurementNote = "proxies measured from tree-sitter symbol tables (signature/type/visibility) plus exported add/remove (api-surface); a body-only change fires nothing — the declared A5 recall gap"

// MeasureBehavioralProxies measures the deterministic behavioral proxies for one
// before/after TypeScript source pair.
//
// Pure and deterministic; the empty string is the absent side (added/deleted
// file). Fail-closed (D14): an extraction failure is returned as an error,
// never as a silently-empty measurement.
func MeasureBehavioralProxies(before string, after string) (BehavioralProxyMeasurement, error) {
	beforeTable := SymbolTable{Symbols: []ExtractedSymbol{}}
	if before != "" {
		table, err := ExtractSymbols(before)
		if err != nil {
			return BehavioralProxyMeasurement{}, err
		}
		beforeTable = table
	}
	afterTable := SymbolTable{Symbols: []ExtractedSymbol{}}
	if after != "" {
		table, err := ExtractSymbols(after)
		if err != nil {
			return BehavioralProxyMeasurement{}, err
		}
		afterTable = table
	}
	diff := DiffSymbolTables(&beforeTable, &afterTable)

	fired := map[string]bool{}
	for _, change := range diff.Changed {
		for _, kind := range change.ChangeKinds {
			fired[string(kind)] = true
		}
	}
	for _, symbol := range diff.Added {
		if symbol.Exported {
			fired["api-surface"] = true
			break
		}
	}
	for _, symbol := range diff.Removed {
		if symbol.Exported {
			fired["api-surface"] = true
			break
		}
	}
	firedProxies := make([]string, 0, len(fired))
	for proxy := range fired {
		firedProxies = append(firedProxies, proxy)
	}
	sort.Strings(firedProxies)
	return BehavioralProxyMeasurement{
		ProxyFired:   len(firedProxies) > 0,
		FiredProxies: firedProxies,
		Diff:         diff,
	}, nil
}
