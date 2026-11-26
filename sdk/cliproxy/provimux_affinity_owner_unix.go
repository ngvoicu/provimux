//go:build !windows

package cliproxy

import (
	"io/fs"
	"os"
	"syscall"
)

// provimuxAffinityUnixModes: the key's mode bits say who can read it.
const provimuxAffinityUnixModes = true

// provimuxAffinityOwnedByUs reports whether a file belongs to the user running the gateway
// (a variable so a test can play another owner).
var provimuxAffinityOwnedByUs = func(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
