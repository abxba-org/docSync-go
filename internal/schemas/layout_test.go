package schemas_test

// Ported from packages/schemas/test/layout.test.ts — canonical `.docsync/`
// layout constants (D1; ARCH §6.3), including the P12-T13 regression:
// org-policy.yml is an OPTIONAL human-owned config file that joins
// CONFIG_FILES without joining any required set.

import (
	"sort"
	"testing"

	"github.com/DUDE616/docSync-go/internal/schemas"
	"github.com/stretchr/testify/assert"
)

func TestLayoutAcceptsOrgPolicyAsTopLevelConfigEntry(t *testing.T) {
	assert.Contains(t, schemas.ConfigFiles, "org-policy.yml")
}

func TestLayoutListsExactlyTheSixCanonicalConfigFilesInOrder(t *testing.T) {
	assert.Equal(t, []string{
		"manifest.yml",
		"policy.yml",
		"concepts.yml",
		"instructions.md",
		"models.yml",
		"org-policy.yml",
	}, schemas.ConfigFiles)
}

func TestLayoutDerivesTopLevelEntryAllowlistCoreClassifiesAgainst(t *testing.T) {
	configEntries := make([]string, 0, len(schemas.ConfigFiles))
	for _, file := range schemas.ConfigFiles {
		configEntries = append(configEntries, schemas.DocSyncDir+"/"+file)
	}
	assert.Contains(t, configEntries, ".docsync/org-policy.yml")
	assert.Len(t, configEntries, 6)
}

func TestLayoutHasNoRequiredOptionalPartition(t *testing.T) {
	// The layout module holds only the six canonical constants — no
	// required/optional partition may ever be added here (the only
	// required-file check is manifest.yml, hard-coded in core's validateLayout).
	names := []string{
		"CONFIG_FILES", "DOC_SYNC_DIR", "MEMORY_FILES",
		"STATE_FILES", "STATE_SUBDIRS", "TOP_LEVEL_DIRS",
	}
	sort.Strings(names)
	assert.Equal(t, []string{
		"CONFIG_FILES", "DOC_SYNC_DIR", "MEMORY_FILES",
		"STATE_FILES", "STATE_SUBDIRS", "TOP_LEVEL_DIRS",
	}, names)
	for _, name := range names {
		assert.NotContains(t, name, "required")
		assert.NotContains(t, name, "Required")
		assert.NotContains(t, name, "REQUIRED")
	}
}

func TestLayoutKeepsOrgPolicyOutOfEveryNonConfigCanonicalSet(t *testing.T) {
	assert.NotContains(t, schemas.MemoryFiles, "org-policy.yml")
	assert.NotContains(t, schemas.StateFiles, "org-policy.yml")
	assert.NotContains(t, schemas.StateSubdirs, "org-policy.yml")
	assert.NotContains(t, schemas.TopLevelDirs, "org-policy.yml")
}
