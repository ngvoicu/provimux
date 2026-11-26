package logging

import "sync/atomic"

// Provimux: who answered (.github/provimux/design/answered-by.md). The switch models.answered-by as it is in force:
// the service sets it from each committed config. Off, no answer and no account list names an account by its marker,
// and every answer is byte for byte what it is without this design.

// provimuxAnsweredBy holds models.answered-by as in force; false until a config sets it.
var provimuxAnsweredBy atomic.Bool

// SetProvimuxAnsweredBy puts models.answered-by in force and returns the previous value.
func SetProvimuxAnsweredBy(on bool) bool { return provimuxAnsweredBy.Swap(on) }

// ProvimuxAnsweredBy reports whether models.answered-by is on.
func ProvimuxAnsweredBy() bool { return provimuxAnsweredBy.Load() }
