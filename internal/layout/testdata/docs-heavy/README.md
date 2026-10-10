# docs-heavy fixture (docSync test kit, P0-T17)

Documentation-dense sample repository consumed by docSync Phase 1-5 test suites
via `createTempRepo("docs-heavy")`. Plain directory tree — not a git repo.

The manifest maps exactly 6 of the 8 markdown documents under `docs/`; the two
remaining docs (`docs/legacy/old-guide.md`, `docs/orphan.md`) are deliberately
UNMAPPED so suites can exercise D7 unknown-coverage behavior (allowlist:
manifest-absent docs are excluded from candidates and flagged as unknown
coverage). Suites assert `exactly 2 unmapped docs` in
`packages/core/test/fixtures.test.ts`.

All content is ASCII, deterministic, and network-free.
