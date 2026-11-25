// Signing and verification of the gate_session cookie value.
//
// Value format: base64url(user|unix-seconds) + "." + base64url(HMAC-SHA256(secret, user|unix-seconds))

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SessionSecret is the HMAC-SHA256 key that signs gate_session cookies. main
// assigns it at start-up. While it is empty, Verify always fails.
var SessionSecret []byte

// ErrInvalidCookie means that a cookie value is malformed, was tampered with,
// has expired, or names a user who does not exist.
var ErrInvalidCookie = errors.New("invalid session cookie")

// Sign produces the gate_session cookie value.
func Sign(user string, expires time.Time) string {
	payload := user + "|" + strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, SessionSecret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify checks a value produced by Sign and, when every check passes, returns
// the user name. The signature is compared with hmac.Equal, in constant time;
// while SessionSecret is empty, Verify always fails.
func Verify(value string, userExists func(string) bool) (string, error) {
	if len(SessionSecret) == 0 {
		return "", fmt.Errorf("%w: empty session secret", ErrInvalidCookie)
	}
	payloadB64, sigB64, ok := strings.Cut(value, ".")
	if !ok {
		return "", fmt.Errorf("%w: missing separator", ErrInvalidCookie)
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return "", fmt.Errorf("%w: bad payload encoding", ErrInvalidCookie)
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", fmt.Errorf("%w: bad signature encoding", ErrInvalidCookie)
	}

	mac := hmac.New(sha256.New, SessionSecret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", fmt.Errorf("%w: signature mismatch", ErrInvalidCookie)
	}

	user, expStr, ok := strings.Cut(string(payload), "|")
	if !ok {
		return "", fmt.Errorf("%w: bad payload format", ErrInvalidCookie)
	}
	expires, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("%w: bad expiry", ErrInvalidCookie)
	}
	if time.Now().After(time.Unix(expires, 0)) {
		return "", fmt.Errorf("%w: expired", ErrInvalidCookie)
	}
	if !userExists(user) {
		return "", fmt.Errorf("%w: unknown user %q", ErrInvalidCookie, user)
	}
	return user, nil
}
