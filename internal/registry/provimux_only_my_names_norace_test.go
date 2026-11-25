//go:build !race

package registry

// namesRaceDetector: timing bounds are not checked under the race detector.
const namesRaceDetector = false
