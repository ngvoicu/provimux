package executor

import (
	"context"
	"sync/atomic"
)

type downstreamWebsocketContextKey struct{}
type requireUpstreamWebsocketContextKey struct{}
type upstreamAttemptTrackerContextKey struct{}
type localWebsocketTurnTrackerContextKey struct{}
type continuityWaitObserverContextKey struct{}

// ContinuityWaitObserver notifies a downstream transport when a real continuity wait starts and ends.
type ContinuityWaitObserver interface {
	OnContinuityWaitStart()
	OnContinuityWaitEnd()
}

type upstreamAttemptTracker struct {
	attempted atomic.Bool
	// pluginBytes: the bytes the attempt returns came out of plugin code.
	pluginBytes atomic.Bool
}

type localWebsocketTurnTracker struct {
	executed atomic.Bool
}

// WithDownstreamWebsocket marks the current request as coming from a downstream websocket connection.
func WithDownstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, downstreamWebsocketContextKey{}, true)
}

// DownstreamWebsocket reports whether the current request originates from a downstream websocket connection.
func DownstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(downstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithRequiredUpstreamWebsocket marks a request whose incremental context is valid only on the current upstream websocket.
func WithRequiredUpstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requireUpstreamWebsocketContextKey{}, true)
}

// RequiredUpstreamWebsocket reports whether falling back to an HTTP upstream would lose request context.
func RequiredUpstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(requireUpstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithUpstreamAttemptTracker installs a fresh tracker for one provider execution attempt.
func WithUpstreamAttemptTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamAttemptTrackerContextKey{}, &upstreamAttemptTracker{})
}

// MarkUpstreamAttempt records that the provider execution reached an upstream transport boundary.
func MarkUpstreamAttempt(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.attempted.Store(true)
}

// MarkPluginBytes records that the bytes this attempt returns came out of plugin code: a plugin executes the attempt,
// or the attempt's response was handed to a plugin's response translator or normalizer. It is called before the
// plugin runs, so nothing the plugin does afterwards can make the attempt nameable. Without a tracker on ctx it does
// nothing.
func MarkPluginBytes(ctx context.Context) {
	if ctx == nil {
		return
	}
	if tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker); ok && tracker != nil {
		tracker.pluginBytes.Store(true)
	}
}

// UpstreamAnswered reports whether the attempt's response can be stated as its credential's: the attempt reached its
// upstream, and its bytes did not come out of plugin code (MarkPluginBytes). Bytes that did name nobody.
func UpstreamAnswered(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	return ok && tracker != nil && tracker.attempted.Load() && !tracker.pluginBytes.Load()
}

// UpstreamAttempted reports whether the tracked provider execution reached an upstream transport boundary.
func UpstreamAttempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	return ok && tracker != nil && tracker.attempted.Load()
}

// WithLocalWebsocketTurnTracker installs a tracker that records when a downstream
// websocket turn was fulfilled locally over HTTP rather than establishing or
// owning an upstream websocket response.
func WithLocalWebsocketTurnTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, localWebsocketTurnTrackerContextKey{}, &localWebsocketTurnTracker{})
}

// MarkLocalWebsocketTurn records that the current downstream websocket turn was
// answered locally without creating an upstream websocket response.
func MarkLocalWebsocketTurn(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.executed.Store(true)
}

// ResetLocalWebsocketTurn clears the local websocket turn marker before a new auth attempt.
func ResetLocalWebsocketTurn(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.executed.Store(false)
}

// LocalWebsocketTurnExecuted reports whether the current downstream websocket turn
// was fulfilled locally rather than on an upstream websocket.
func LocalWebsocketTurnExecuted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	return ok && tracker != nil && tracker.executed.Load()
}

// WithContinuityWaitObserver attaches an observer notified only while a real continuity wait is active.
func WithContinuityWaitObserver(ctx context.Context, observer ContinuityWaitObserver) context.Context {
	if observer == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, continuityWaitObserverContextKey{}, observer)
}

// NotifyContinuityWaitStart notifies any attached observer that a real continuity wait has started.
func NotifyContinuityWaitStart(ctx context.Context) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(continuityWaitObserverContextKey{}).(ContinuityWaitObserver); ok && observer != nil {
		observer.OnContinuityWaitStart()
	}
}

// NotifyContinuityWaitEnd notifies any attached observer that a continuity wait has ended.
func NotifyContinuityWaitEnd(ctx context.Context) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(continuityWaitObserverContextKey{}).(ContinuityWaitObserver); ok && observer != nil {
		observer.OnContinuityWaitEnd()
	}
}
