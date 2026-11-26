//go:build windows

package cliproxy

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// provimuxAffinityUnixModes: Windows reports no meaningful mode bits.
const provimuxAffinityUnixModes = false

// provimuxAffinityOwnedByUs: Windows has no Unix owner or mode bits; the file's access control
// list (inherited from the auth folder) decides who reads it, and is not inspected.
var provimuxAffinityOwnedByUs = func(fs.FileInfo) bool { return true }

var errProvimuxAffinityLocked = errors.New("locked by another writer")

// provimuxAffinityLockFile takes an exclusive lock on file without waiting, on one byte far past
// its end (so reading the key is never blocked). It ends when the file is closed or the process
// ends.
func provimuxAffinityLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	overlapped.OffsetHigh = 0x40000000
	errLock := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errLock != nil {
		if errors.Is(errLock, windows.ERROR_LOCK_VIOLATION) || errors.Is(errLock, windows.ERROR_IO_PENDING) {
			return errProvimuxAffinityLocked
		}
		return errLock
	}
	return nil
}
