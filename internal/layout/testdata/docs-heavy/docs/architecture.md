# Architecture

Mapped in `suggest` mode. The service is split into an intake API, a
normalization pipeline, and a storage sink. Every boundary is an explicit
interface so analyzers can diff contracts independently.
