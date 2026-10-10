# payments fixture (docSync test kit, P0-T17)

Payments-style sample repository consumed by docSync Phase 1-5 test suites via
`createTempRepo("payments")` (`@docsync/core/testing`). Plain directory tree —
not a git repo, snippets are not compiled as a project.

Contents:

- `src/payments/refund.ts` — refund flow snippet containing the `idempotencyKey`
  marker line (behavioral line flagged for later-phase drift tests).
- `src/payments/types.ts`, `src/api/payments/client.ts`, `src/auth/credentials.ts` —
  supporting source snippets.
- `docs/payments.md` — documents idempotency-key behavior; deliberately contains
  claims that will drift when later phases mutate `refund.ts`.
- `docs/security.md` — locked-mode example document (never modified, only reported).
- `docs/architecture.md` — suggest-mode architecture overview.
- `.docsync/manifest.yml` — manifest validated against `MANIFEST_SCHEMA`
  (`@docsync/schemas`) in `packages/core/test/fixtures.test.ts`.

All content is ASCII, deterministic, and network-free.
