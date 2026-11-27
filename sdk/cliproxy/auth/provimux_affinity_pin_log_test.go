package auth

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Note 5 of the fourth review: while the pin's samples run, the process's log level stays as it is, so another
// goroutine's info lines all reach the log, and the samples' own selector lines reach it not at all. The observer
// runs until it has looked at the level many times while a pin was running: the count of looks decides, not a clock.
func TestProvimuxAffinityPinKeepsOtherLines(t *testing.T) {
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	defer log.SetLevel(previous)
	output := log.StandardLogger().Out
	log.SetOutput(io.Discard) // the hook counts the lines; the test's output need not hold them
	defer log.SetOutput(output)
	hook := logtest.NewGlobal()
	defer hook.Reset()
	samples := provimuxAffinityPinSamples()

	var pinning atomic.Bool
	var looksDuring, lowered, emitted atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			during := pinning.Load()
			if !log.IsLevelEnabled(log.InfoLevel) {
				lowered.Add(1)
			}
			log.Info("provimux-test line of another goroutine")
			emitted.Add(1)
			if during && pinning.Load() {
				looksDuring.Add(1)
			}
		}
	}()
	const wantLooks = 200
	for runs := 0; looksDuring.Load() < wantLooks; runs++ {
		if runs == 100000 {
			break
		}
		pinning.Store(true)
		if _, err := provimuxAffinityRunPin(samples); err != nil {
			t.Fatal(err)
		}
		pinning.Store(false)
	}
	close(stop)
	wg.Wait()
	if looksDuring.Load() < wantLooks {
		t.Fatalf("premise: the observer looked only %d times while a pin ran", looksDuring.Load())
	}
	received, selectorLines := 0, 0
	for _, entry := range hook.AllEntries() {
		switch {
		case entry.Message == "provimux-test line of another goroutine":
			received++
		case len(entry.Message) > len("session-affinity:") && entry.Message[:len("session-affinity:")] == "session-affinity:":
			selectorLines++
		}
	}
	if lowered.Load() != 0 || int64(received) != emitted.Load() {
		t.Fatalf("while the pin ran the level was lowered at %d looks; %d of %d lines of another goroutine reached the log", lowered.Load(), received, emitted.Load())
	}
	if selectorLines != 0 {
		t.Fatalf("%d lines of the pin's own selectors reached the log", selectorLines)
	}
	if log.GetLevel() != log.InfoLevel {
		t.Fatalf("level after: %s", log.GetLevel())
	}
}
