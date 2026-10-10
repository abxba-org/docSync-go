// Ported from packages/schemas/src/v1/layout.ts: canonical `.docsync/` layout
// constants (ARCH §6.3, decision D1) — shared with core so every package
// references one definition of the tree. Pure constants: no fs.
//
// No required/optional partition lives here: presence is optional for EVERY
// entry. The canonical required-file check lives in core's layout validator
// (packages/core/src/layout/validate.ts) and hard-codes manifest.yml alone
// (D7/D8: no manifest, no run). org-policy.yml (P12-T13) follows that rule —
// absent means "no org floors", never a layout error.
package schemas

// DocSyncDir is the `.docsync/` directory name (DOC_SYNC_DIR).
const DocSyncDir = ".docsync"

// ConfigFiles are the human-owned, PR-reviewed config files at the
// `.docsync/` root. org-policy.yml is OPTIONAL (P12-T13): absent = no org
// floors; it never joins a required set (CONFIG_FILES).
var ConfigFiles = []string{
	"manifest.yml",
	"policy.yml",
	"concepts.yml",
	"instructions.md",
	"models.yml",
	"org-policy.yml",
}

// MemoryFiles are the persistent repository memory files under `memory/`
// (MEMORY_FILES).
var MemoryFiles = []string{
	"decisions.md",
	"known-patterns.md",
	"exceptions.md",
	"failures.md",
}

// StateSubdirs are the system-written state subdirectories under `state/`
// (STATE_SUBDIRS).
var StateSubdirs = []string{"baselines", "claims", "docs", "runs"}

// StateFiles are the system-written state files at the `state/` root
// (STATE_FILES).
var StateFiles = []string{"audit-cursor"}

// TopLevelDirs are the top-level `.docsync/` directories. policy-history/
// sits at the TOP level — never under memory/ (D1 tree in ARCH §6.3).
var TopLevelDirs = []string{"evaluation", "memory", "policy-history", "state"}
