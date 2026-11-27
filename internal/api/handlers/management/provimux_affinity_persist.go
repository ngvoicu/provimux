package management

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// Provimux-only: GET /v8/management/provimux/session-affinity-persist. Whether session affinity's
// bindings are kept across a restart (routing.session-affinity-persist), and if not, why: from the
// configuration "session-affinity-off", "home-mode", "kv-store-off" or "switch-off" (set to false;
// not set, it is on); from the
// files "foreign-file", "key-not-private", "directory-not-writable", "path-unusable" or "in-use". Also the
// two files' paths, when they were last written and what the start restored. The switch itself is
// read and written at /v8/management/config/routing/session-affinity-persist. Read-only: the
// state is a copy kept in memory, the request never waits on the disk.

var (
	provimuxAffinityPersistMu     sync.RWMutex
	provimuxAffinityPersistSource func() any
)

// SetProvimuxAffinityPersistSource registers the reader of the state; sdk/cliproxy provides it.
func SetProvimuxAffinityPersistSource(source func() any) {
	provimuxAffinityPersistMu.Lock()
	defer provimuxAffinityPersistMu.Unlock()
	provimuxAffinityPersistSource = source
}

// ProvimuxAffinityPersist answers the state of routing.session-affinity-persist.
func (h *Handler) ProvimuxAffinityPersist(c *gin.Context) {
	provimuxAffinityPersistMu.RLock()
	source := provimuxAffinityPersistSource
	provimuxAffinityPersistMu.RUnlock()
	if source == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "not available"})
		return
	}
	c.JSON(http.StatusOK, source())
}
