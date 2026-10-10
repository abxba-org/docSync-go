package gitpr

// Package entry wiring (ported from the gitpr sub-barrel's role, NOT the
// barrel itself — index.ts is a pure re-export and orchestrator-owned per
// CONVENTIONS.md "One writer per file").
//
// This file carries the minimal LOCAL SEAM this unit declares while the owning
// delegate lands pr-platform.go in the same package. It is deliberately
// `Seam`-prefixed so nothing here can collide with the real export when it
// arrives; the orchestrator deletes the seam and rewires the (two) call sites.
//
// TODO(seam:pr-platform): replace SeamPrRef with pr-platform.go's PrRef
// (pr-platform.ts: number/branch/base/title/body/state/updatedAt) — the
// consumers are supersede.go's SupersedePrClient and SupersedeStalePrInput.
//
// Resolved seams (the owning files landed while this unit was being written, so
// their symbols are consumed DIRECTLY and no local mirror remains):
//   - branch-factory.go  → MAX_DISAMBIGUATOR_CANDIDATES, CodeBranchName
//   - commit-assembly.go → DocFileApplier

// SeamPrRef is a platform PR, structurally typed — the minimal mirror of
// pr-platform.ts's PrRef this package consumes today (the supersede flow needs
// the number; the full shape is mirrored so the swap is a rename).
type SeamPrRef struct {
	Number    int    `json:"number"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"` // 'open' | 'closed'
	UpdatedAt string `json:"updated_at"`
}
