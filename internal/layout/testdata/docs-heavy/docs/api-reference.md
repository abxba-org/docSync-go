# API Reference

Mapped in `auto` mode. The public HTTP API exposes `POST /events` (ingest a
batch), `GET /events/{id}` (fetch one event), and `GET /health` (liveness).
Batch size limit is 1000 events; larger batches are rejected with `413`.
