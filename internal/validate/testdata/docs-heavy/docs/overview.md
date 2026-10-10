# Service Overview

Mapped by `.docsync/manifest.yml` in `auto` mode. The ingestion service accepts
events, normalizes them, and forwards them to storage. Delivery is
at-least-once; consumers must be idempotent.
