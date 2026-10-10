// Package memory ports the docSync memory subsystem (P10-T20..T21) from
// TypeScript to Go: the D1 EXACTLY-FOUR `.docsync/memory/` layout, its
// evidence-backed record store with R12 contradiction decay, and the INV-7
// feed projection.
//
// Ports (one Go file per TypeScript module, plus the single test suite):
//
//   - layout.go      ← packages/core/src/memory/layout.ts  (+ the index.ts barrel)
//   - records.go     ← packages/core/src/memory/records.ts
//   - feeds.go       ← packages/core/src/memory/feeds.ts
//   - memory_test.go ← packages/core/test/memory.test.ts
//
// The record schema itself is NOT redeclared here: MemoryRecord, the
// MemoryFile stems, MEMORY_RECORD_SCHEMA validation and the strict
// YAML/JSON parser all come from internal/schemas (memory-record.go, the port
// of @docsync/schemas' memory-record.ts) and are reused as-is. ULIDs are
// minted by docutil.ULIDGenerator (port of core/src/util/ulid.ts), and errors
// are carried as docutil coded errors.
//
// Behavior preserved from the references:
//
//   - D1 (ARCH §8.8, CTX §25, PLAN §11.6): `memory/` holds EXACTLY FOUR
//     Markdown files — decisions.md, known-patterns.md, exceptions.md,
//     failures.md. Enforcement creates MISSING files as empty scaffolds and
//     REPORTS extra entries; extra files are never auto-deleted (deletion is
//     a human decision).
//   - Record format: appended `---` fenced YAML front-matter blocks followed
//     by the observation/action prose projection. The front-matter is the
//     single source of truth and is schema-validated on every read; any
//     violation is a typed, fail-closed error (D6/D8), never a skipped block.
//   - R12 (memory cannot ossify wrong beliefs): contradict appends a failure
//     record to failures.md carrying `contradiction_of` AND halves the
//     contradicted record's confidence, floored at MEMORY_CONFIDENCE_FLOOR.
//   - INV-7 (memory never mutates policy): the feeds are a pure projection of
//     parsed records into plain data; no I/O, no write path, no policy handle.
//
// Serialization fidelity: the front-matter YAML is emitted to match the
// TypeScript `yaml` package (`stringify(fields, { lineWidth: 0 })`, eemeli/yaml
// v2) byte-for-byte over the record schema's value domain — plain scalars,
// `|-`/`|`/`|+` literal block scalars, single/double quoting per the same
// indicator and core-schema resolution rules, and JS number-to-string
// formatting. memory_test.go pins the golden bytes captured from that package.
package memory
