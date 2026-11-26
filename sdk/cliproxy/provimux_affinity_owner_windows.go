//go:build windows

package cliproxy

import "io/fs"

// provimuxAffinityUnixModes: Windows reports no meaningful mode bits.
const provimuxAffinityUnixModes = false

// provimuxAffinityOwnedByUs: Windows has no Unix owner or mode bits; the file's ACL (inherited
// from the auth folder) decides who reads it.
var provimuxAffinityOwnedByUs = func(fs.FileInfo) bool { return true }
