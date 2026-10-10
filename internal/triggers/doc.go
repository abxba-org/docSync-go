// Package triggers ports the docSync trigger-taxonomy mapping layer (P0-T09)
// from packages/core/src/triggers/map.ts: versioned resolution of the manifest
// trigger vocabulary onto the engine decision schema (R17), with
// version-naming unknown-value errors (V-P0-9).
//
// The v1 user vocabulary IS the universal enum — mapping is identity plus
// validation against the universal trigger set (internal/schemas). Future
// taxonomy versions remap through a registered trigger-map schema
// (docsync.trigger-map); the resolution version is named in every unknown-value
// error. Unknown values never guess and never clamp to a near match (D6/D8):
// they are reported, and the diagnostic names the expected taxonomy version and
// the versioned decision-schema reference verbatim.
//
// This package depends only on internal/schemas (the universal trigger
// vocabulary + the registered trigger-map example). The taxonomy VERSION is
// pinned here as a local constant mirroring schemas.TriggerTaxonomyVersion
// (unexported there); it is asserted against the registered docsync.trigger-map
// entry's version in the tests so drift fails closed.
package triggers
