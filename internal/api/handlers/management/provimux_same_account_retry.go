package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux-only: GET /v8/management/provimux/same-account-retry. The value of routing.retry.same-account-retry in
// use, whether the file sets it, and what the rule did since the gateway started (one second try on the same account
// when the vendor refused a request as overloaded: sdk/cliproxy/auth/provimux_same_account_retry.go). Read-only; the value is written
// through the generic config path (/v8/management/config/routing/retry/same-account-retry).

type provimuxSameAccountRetryResponse struct {
	ObservedAt string `json:"observed_at"`
	// Enabled is the value in use; SetInFile whether the config file sets it (else it is the default, Default).
	Enabled   bool `json:"enabled"`
	SetInFile bool `json:"set_in_file"`
	Default   bool `json:"default"`
	// Applies is false where the gateway never retries even when enabled: in Home mode (Home owns the accounts).
	Applies bool `json:"applies"`
	// The wait before the second try without a Retry-After, and the longest Retry-After that still counts as passing.
	WaitMinMS       int64 `json:"wait_min_ms"`
	WaitMaxMS       int64 `json:"wait_max_ms"`
	RetryAfterMaxMS int64 `json:"retry_after_max_ms"`
	// Since the process started (each count read on its own, not one snapshot): second tries sent; of those, answered
	// (a complete answer, or a stream that began) and failed; and refusals after which the client left during the wait.
	Since               string `json:"since"`
	SecondTriesSent     uint64 `json:"second_tries_sent"`
	SecondTriesAnswered uint64 `json:"second_tries_answered"`
	SecondTriesFailed   uint64 `json:"second_tries_failed"`
	LeftDuringWait      uint64 `json:"left_during_wait"`
}

// ProvimuxSameAccountRetry handles GET /v8/management/provimux/same-account-retry.
func (h *Handler) ProvimuxSameAccountRetry(c *gin.Context) {
	cfg := h.configSnapshot()
	enabled, set := cfg.ProvimuxSameAccountRetry()
	home := cfg != nil && cfg.Home.Enabled
	waitMin, waitMax := coreauth.ProvimuxRetryWaitBounds()
	stats := coreauth.ProvimuxSameAccountRetryStats()
	c.JSON(http.StatusOK, provimuxSameAccountRetryResponse{
		ObservedAt:          provimuxTime(provimuxNow()),
		Enabled:             enabled,
		SetInFile:           set,
		Default:             config.ProvimuxSameAccountRetryProcessDefault(),
		Applies:             enabled && !home,
		WaitMinMS:           waitMin.Milliseconds(),
		WaitMaxMS:           waitMax.Milliseconds(),
		RetryAfterMaxMS:     coreauth.ProvimuxRetryAfterMax.Milliseconds(),
		Since:               provimuxTime(stats.Since),
		SecondTriesSent:     stats.SecondTriesSent,
		SecondTriesAnswered: stats.SecondTriesAnswered,
		SecondTriesFailed:   stats.SecondTriesFailed,
		LeftDuringWait:      stats.LeftDuringWait,
	})
}
