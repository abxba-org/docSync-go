# Architecture

Mapped by `.docsync/manifest.yml` in `suggest` mode from `src/**`.

The payments service is a thin API layer over a ledger database. Modules:

- `src/payments` — domain logic: captures, refunds, idempotency rules.
- `src/api/payments` — HTTP client wrappers used by upstream callers.
- `src/auth` — credential loading and session validation.

Architecture decisions are recorded outside this document; this overview is a
suggest-mode summary proposed for review, never auto-applied.
