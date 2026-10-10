# Payments

Behavior of the payments service. Mapped by `.docsync/manifest.yml` in `auto`
mode from `src/payments/**` and `src/api/payments/**`.

## Refunds

Refund requests require an `idempotencyKey`.

- The same idempotency key always returns the same refund result; retries never
  create a second refund.
- Retries are safe within a 24-hour idempotency window.
  <!-- DRIFT-PRONE CLAIM (P0-T17): the 24-hour window exists so later-phase tests
       can change refund.ts and observe docs/payments.md becoming stale. -->
- A refund whose `idempotencyKey` is missing is rejected with a validation error
  before any money moves.

## Captures

Captures are recorded with the exact minor-unit amount that was approved.
Partial captures are rejected; use refunds for partial returns.
