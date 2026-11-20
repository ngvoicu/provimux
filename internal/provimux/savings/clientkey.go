package savings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"unicode/utf8"
)

// keyID is the first 16 hex characters of HMAC-SHA-256(secret, key); "" for an empty key.
// Without the store's secret an id tells nothing about the key (01-recording.md section 7).
func keyID(secret []byte, key string) string {
	if key == "" {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// keyTail is the key's last 4 characters when it has 16 or more, else "".
func keyTail(key string) string {
	if utf8.RuneCountInString(key) < 16 {
		return ""
	}
	runes := []rune(key)
	return string(runes[len(runes)-4:])
}
