package main

import (
	"testing"
	"time"
)

// setSecret replaces the package-level SessionSecret and restores the
// original when the test ends.
func setSecret(t *testing.T, secret []byte) {
	t.Helper()
	orig := SessionSecret
	SessionSecret = secret
	t.Cleanup(func() { SessionSecret = orig })
}

// allowAll serves as the userExists callback: no restriction on users.
func allowAll(string) bool { return true }

func TestSignVerifyRoundTrip(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	value := Sign("alice", time.Now().Add(time.Hour))
	if value == "" {
		t.Fatal("Sign returned empty value")
	}
	user, err := Verify(value, allowAll)
	if err != nil {
		t.Fatalf("Verify: unexpected error: %v", err)
	}
	if user != "alice" {
		t.Fatalf("Verify user = %q, want %q", user, "alice")
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	value := Sign("alice", time.Now().Add(time.Hour))

	// Tamper with the last character of the signature. The last character
	// carries only 4 significant bits (the low 2 are base64 padding bits), and
	// 'A', 'B', 'C' and 'D' all decode to the same bytes, so when last is 'A'
	// the replacement has to be 'E', which differs in the significant bits.
	last := value[len(value)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'E'
	}
	tampered := value[:len(value)-1] + string(repl)
	if tampered == value {
		t.Fatal("tampering did not change the value")
	}

	if _, err := Verify(tampered, allowAll); err == nil {
		t.Fatal("Verify(tampered): want error, got nil")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	value := Sign("alice", time.Now().Add(-time.Minute))
	if _, err := Verify(value, allowAll); err == nil {
		t.Fatal("Verify(expired): want error, got nil")
	}
}

func TestVerifyRejectsUnknownUser(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	value := Sign("alice", time.Now().Add(time.Hour))
	if _, err := Verify(value, func(string) bool { return false }); err == nil {
		t.Fatal("Verify(unknown user): want error, got nil")
	}
}

func TestVerifyMalformed(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	for _, bad := range []string{
		"",          // empty value
		"garbage",   // no separator
		"no-dot",    // no separator
		"YQ.YQ",     // both parts decode, but the signature does not match
		"////.////", // '/' is not in the base64url alphabet
	} {
		if _, err := Verify(bad, allowAll); err == nil {
			t.Errorf("Verify(%q): want error, got nil", bad)
		}
	}
}

func TestVerifyRejectsEmptySecret(t *testing.T) {
	setSecret(t, []byte("test-secret"))
	value := Sign("alice", time.Now().Add(time.Hour))

	SessionSecret = nil
	if _, err := Verify(value, allowAll); err == nil {
		t.Fatal("Verify with empty SessionSecret: want error, got nil")
	}
}
