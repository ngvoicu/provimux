package cliproxy

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: the key of the account markers (.github/provimux/design/answered-by.md 2.2). 32 random bytes, made once
// and kept in the gateway's local key-value store (server.kv-store, a bbolt file beside the logins) under a key of its
// own; read once, before the first request is served: a local file, never a network call. It is not Savings' secret.
// Where no local store keeps it (the store disabled, or not opened because another gateway holds the file), the key is
// random for the run: the markers hold until the process ends, and the log says why, once. Removing the stored key
// gives every account a new marker. The key, and anything made from it, is never logged.
//
// Home mode writes no marker at all (answered-by review): each node would have its own key, answers are named from
// credentials Home hands out per request, and the lists come from the node's own manager, so two nodes would name one
// account differently. Until the key can live in Home's store, the switch stays off there and no key is made.

// provimuxAnsweredKeyName is the key-value store's key under which the marker key is kept.
const provimuxAnsweredKeyName = "provimux:answered:v1:key"

// provimuxAnsweredKeySize is the size of the marker key in bytes.
const provimuxAnsweredKeySize = 32

// provimuxAnsweredKeyRandom fills a new key; tests replace it to know the key they look for in the log.
var provimuxAnsweredKeyRandom = func(key []byte) { _, _ = rand.Read(key) } // crypto/rand.Read never fails on Go's platforms

// provimuxAnsweredKeyStore is the part of the local store the key needs; *kvstore.BoltStore is one.
type provimuxAnsweredKeyStore interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

var errProvimuxAnsweredKeyNoStore = errors.New("no local key-value store")

// provimuxAnsweredKeyFrom returns the key kept in store, making and keeping it when the store holds none (the first
// writer wins: the key returned is the one the store holds). made is true when this call made the key the store now
// holds. kept is false when the key is random for this run: no store, a store that cannot be read or written, or a
// stored value that is not a key, which is left as it is; err then says why, without the key.
func provimuxAnsweredKeyFrom(ctx context.Context, store provimuxAnsweredKeyStore) (key []byte, kept, made bool, err error) {
	random := func() []byte {
		fresh := make([]byte, provimuxAnsweredKeySize)
		provimuxAnsweredKeyRandom(fresh)
		return fresh
	}
	if store == nil {
		return random(), false, false, errProvimuxAnsweredKeyNoStore
	}
	stored, found, errGet := store.KVGet(ctx, provimuxAnsweredKeyName)
	if errGet != nil {
		return random(), false, false, fmt.Errorf("the local key-value store could not be read: %w", errGet)
	}
	if !found {
		fresh := random()
		wrote, errSet := store.KVSetNX(ctx, provimuxAnsweredKeyName, fresh, 0)
		if errSet != nil {
			return random(), false, false, fmt.Errorf("the local key-value store could not be written: %w", errSet)
		}
		if stored, found, errGet = store.KVGet(ctx, provimuxAnsweredKeyName); errGet != nil {
			return random(), false, false, fmt.Errorf("the local key-value store could not be read: %w", errGet)
		}
		if !found {
			return random(), false, false, errors.New("the key written to the local key-value store did not read back")
		}
		made = wrote
	}
	if len(stored) != provimuxAnsweredKeySize {
		return random(), false, false, fmt.Errorf("the value stored under %s is not a key (it is left as it is)", provimuxAnsweredKeyName)
	}
	return append([]byte(nil), stored...), true, made, nil
}

var provimuxAnsweredKeyState struct {
	sync.Mutex
	loaded bool
}

// provimuxHomeMode reports whether the gateway runs as a Home node.
func provimuxHomeMode(cfg *config.Config) bool {
	return cfg != nil && cfg.Home.Enabled || home.Current() != nil
}

// provimuxLoadAnsweredKey reads the marker key from the local store as configured now for cfg, once per process, and
// gives it to the markers. It runs where the service configures the store, which a start does before it serves. In
// Home mode it makes no key (and may run again if the gateway leaves Home mode).
func provimuxLoadAnsweredKey(cfg *config.Config) {
	provimuxAnsweredKeyState.Lock()
	defer provimuxAnsweredKeyState.Unlock()
	if provimuxAnsweredKeyState.loaded || provimuxHomeMode(cfg) {
		return
	}
	provimuxAnsweredKeyState.loaded = true
	var store provimuxAnsweredKeyStore
	why := errProvimuxAnsweredKeyNoStore
	client, enabled, errClient := kvstore.CurrentKVClient()
	switch bolt, isBolt := client.(*kvstore.BoltStore); {
	case errClient != nil:
		why = fmt.Errorf("the local key-value store could not be opened (another gateway may hold its file): %w", errClient)
	case isBolt && bolt != nil:
		store = bolt
	case cfg != nil && cfg.KVStore.Disabled:
		why = errors.New("the local key-value store is disabled (server.kv-store.disabled)")
	case !enabled:
		why = errors.New("the local key-value store is not in use")
	default:
		why = errors.New("the key-value store in use is not local")
	}
	key, kept, made, errKey := provimuxAnsweredKeyFrom(context.Background(), store)
	coreauth.SetProvimuxMarkerKey(key)
	switch {
	case !kept && store == nil:
		log.Warnf("answered-by: the key of the account markers is not kept: %v; the markers hold until this process ends", why)
	case !kept:
		log.Warnf("answered-by: the key of the account markers is not kept: %v; the markers hold until this process ends", errKey)
	case made:
		log.Infof("answered-by: made the key of the account markers and kept it in the local key-value store (%s); the markers hold across restarts while that store is kept", provimuxAnsweredKeyName)
	}
}

// provimuxAnsweredByHomeLogged says whether the log said once that Home mode writes no marker.
var provimuxAnsweredByHomeLogged atomic.Bool

// provimuxSetAnsweredBy puts models.answered-by of cfg in force, where the service commits a config and from the
// first config of a start, which is not committed. In Home mode it stays off, and the log says so once.
func provimuxSetAnsweredBy(cfg *config.Config) {
	on := cfg != nil && cfg.Models.AnsweredByOn()
	if on && provimuxHomeMode(cfg) {
		on = false
		if provimuxAnsweredByHomeLogged.CompareAndSwap(false, true) {
			log.Info("answered-by: Home mode: no answer and no account list names an account (models.answered-by has no effect until the markers' key can be kept in Home's store)")
		}
	}
	logging.SetProvimuxAnsweredBy(on)
}
