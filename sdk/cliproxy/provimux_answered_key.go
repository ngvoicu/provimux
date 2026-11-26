package cliproxy

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Provimux: the key of the account markers (.github/provimux/design/answered-by.md 2.2). 32 random bytes, made once
// and kept in the gateway's local key-value store (server.kv-store, a bbolt file beside the logins) under a key of its
// own; read once, before the first request is served: a local file, never a network call. It is not Savings' secret.
// Where no local store keeps it (the store disabled or unavailable, Home mode, whose store is remote), the key is
// random for the run: the markers hold until the process ends, and the log says so once. Removing the stored key
// gives every account a new marker. The key is never logged.

// provimuxAnsweredKeyName is the key-value store's key under which the marker key is kept.
const provimuxAnsweredKeyName = "provimux:answered:v1:key"

// provimuxAnsweredKeySize is the size of the marker key in bytes.
const provimuxAnsweredKeySize = 32

// provimuxAnsweredKeyStore is the part of the local store the key needs; *kvstore.BoltStore is one.
type provimuxAnsweredKeyStore interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

var errProvimuxAnsweredKeyNoStore = errors.New("no local key-value store")

// provimuxAnsweredKeyFrom returns the key kept in store, making and keeping it when the store holds none (the first
// writer wins: the key returned is the one the store holds). kept is false when the key is random for this run: no
// store, a store that cannot be read or written, or a stored value that is not a key, which is left as it is.
func provimuxAnsweredKeyFrom(ctx context.Context, store provimuxAnsweredKeyStore) (key []byte, kept bool, err error) {
	random := func() []byte {
		fresh := make([]byte, provimuxAnsweredKeySize)
		_, _ = rand.Read(fresh) // crypto/rand.Read never fails on the platforms Go supports
		return fresh
	}
	if store == nil {
		return random(), false, errProvimuxAnsweredKeyNoStore
	}
	stored, found, errGet := store.KVGet(ctx, provimuxAnsweredKeyName)
	if errGet != nil {
		return random(), false, errGet
	}
	if !found {
		if _, errSet := store.KVSetNX(ctx, provimuxAnsweredKeyName, random(), 0); errSet != nil {
			return random(), false, errSet
		}
		if stored, found, errGet = store.KVGet(ctx, provimuxAnsweredKeyName); errGet != nil {
			return random(), false, errGet
		}
		if !found {
			return random(), false, errors.New("the stored key did not read back")
		}
	}
	if len(stored) != provimuxAnsweredKeySize {
		return random(), false, errors.New("the stored value is not a key")
	}
	return append([]byte(nil), stored...), true, nil
}

var provimuxAnsweredKeyState struct {
	sync.Mutex
	loaded bool
}

// provimuxLoadAnsweredKey reads the marker key from the local store as configured now, once per process, and gives
// it to the markers. It runs where the service configures the store, which a start does before it serves.
func provimuxLoadAnsweredKey() {
	provimuxAnsweredKeyState.Lock()
	defer provimuxAnsweredKeyState.Unlock()
	if provimuxAnsweredKeyState.loaded {
		return
	}
	provimuxAnsweredKeyState.loaded = true
	var store provimuxAnsweredKeyStore
	if client, _, errClient := kvstore.CurrentKVClient(); errClient == nil {
		if bolt, ok := client.(*kvstore.BoltStore); ok && bolt != nil {
			store = bolt
		}
	}
	key, kept, errKey := provimuxAnsweredKeyFrom(context.Background(), store)
	coreauth.SetProvimuxMarkerKey(key)
	if !kept {
		log.Warnf("answered-by: the key of the account markers is not kept (%v); the markers hold until this process ends", errKey)
	}
}
