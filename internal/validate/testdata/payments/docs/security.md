# Security

<!-- LOCKED-MODE EXAMPLE (P0-T17): mapped with mode `locked` in
     .docsync/manifest.yml over src/auth/** and src/security/**. Locked docs are
     detected and reported by docSync but are NEVER modified by it — ownership
     is the floor (locked resolves `blocked` at ANY confidence). -->

## Credentials

API credentials are injected via environment variables only. They are never
logged, never committed, and never passed to model calls. Rotation follows the
organization credential-rotation policy.

## Data handling

Payment data is stored in the ledger database only. Access requires an
audited, short-lived session; service accounts cannot read payment rows.
