//go:build !windows

package cliproxy

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// provimuxAffinityUnixModes: the key's mode bits say who can read it (access control lists are not
// inspected).
const provimuxAffinityUnixModes = true

// provimuxAffinityOwnedByUs reports whether a file belongs to the user running the gateway
// (a variable so a test can play another owner).
var provimuxAffinityOwnedByUs = func(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

var errProvimuxAffinityLocked = errors.New("locked by another writer")

// provimuxAffinityLockFile takes an exclusive lock on file without waiting. The lock belongs to
// this open file: another open of the same file, in this process or another, cannot take it; it
// ends when the file is closed or the process ends.
func provimuxAffinityLockFile(file *os.File) error {
	if errLock := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errLock != nil {
		if errors.Is(errLock, syscall.EWOULDBLOCK) || errors.Is(errLock, syscall.EAGAIN) {
			return errProvimuxAffinityLocked
		}
		return errLock
	}
	return nil
}
