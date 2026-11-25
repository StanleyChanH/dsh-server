package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// hashPw returns the bcrypt hash of a plaintext password, at the production
// cost.
func hashPw(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword(%q): %v", password, err)
	}
	return string(hash)
}

// usersPath returns the path of a users.json inside a temporary directory.
func usersPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "users.json")
}

// writeUsers writes the user table in the real format of users.json: a flat
// map from user name to bcrypt hash.
func writeUsers(t *testing.T, path string, users map[string]string) {
	t.Helper()
	data, err := json.Marshal(users)
	if err != nil {
		t.Fatalf("Marshal users: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

// setNowFunc replaces the package-level nowFunc and restores the original
// when the test ends.
func setNowFunc(t *testing.T, f func() time.Time) {
	t.Helper()
	orig := nowFunc
	nowFunc = f
	t.Cleanup(func() { nowFunc = orig })
}

func TestCheckValidPassword(t *testing.T) {
	path := usersPath(t)
	writeUsers(t, path, map[string]string{"alice": hashPw(t, "correct horse battery")})

	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: unexpected error: %v", err)
	}
	if !users.Check("alice", "correct horse battery") {
		t.Fatal(`Check(alice, "correct horse battery") = false, want true`)
	}
}

func TestCheckWrongPassword(t *testing.T) {
	path := usersPath(t)
	writeUsers(t, path, map[string]string{"alice": hashPw(t, "right-password")})

	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: unexpected error: %v", err)
	}
	cases := []struct{ name, user, pass string }{
		{"WrongPassword", "alice", "wrong-password"},
		{"UnknownUser", "bob", "right-password"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if users.Check(c.user, c.pass) {
				t.Fatalf("Check(%q, %q) = true, want false", c.user, c.pass)
			}
		})
	}
}

func TestReloadOnFileChange(t *testing.T) {
	path := usersPath(t)
	aliceHash := hashPw(t, "alice-password")
	bobHash := hashPw(t, "bob-password")

	var users Users
	writeUsers(t, path, map[string]string{"alice": aliceHash})
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: unexpected error: %v", err)
	}
	if !users.Check("alice", "alice-password") {
		t.Fatal("初次加载：Check(alice) = false, want true")
	}
	if users.Check("bob", "bob-password") {
		t.Fatal("初次加载：Check(bob) = true, want false")
	}

	// Rewrite users.json (the mtime changes) to add bob, without calling
	// LoadUsers again: the running service has to see the new user on the next
	// Check, by reloading on its own.
	writeUsers(t, path, map[string]string{"alice": aliceHash, "bob": bobHash})
	if !users.Check("bob", "bob-password") {
		t.Fatal("改写后：Check(bob) = false, want true（mtime 变化未触发自动重载）")
	}
	if !users.Check("alice", "alice-password") {
		t.Fatal("改写后：Check(alice) = false, want true")
	}

	// Rewrite again and remove alice: the reload has to be a wholesale
	// replacement, so a deleted user is dead at once.
	writeUsers(t, path, map[string]string{"bob": bobHash})
	if users.Check("alice", "alice-password") {
		t.Fatal("移除后：Check(alice) = true, want false")
	}
	if !users.Check("bob", "bob-password") {
		t.Fatal("移除后：Check(bob) = false, want true")
	}
}

func TestRateLimit(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	setNowFunc(t, func() time.Time { return now })

	const ip = "203.0.113.7"
	l := NewLimiter(5, time.Minute)

	if !l.Allowed(ip) {
		t.Fatal("Allowed(尚无失败) = false, want true")
	}

	// After 5 failures from the same IP it is refused (the 6th attempt gets a
	// 429 even with the right password).
	for i := 0; i < 5; i++ {
		l.Fail(ip)
	}
	if l.Allowed(ip) {
		t.Fatal("Allowed(5 次失败后) = true, want false")
	}

	// Another IP is not affected.
	if !l.Allowed("198.51.100.9") {
		t.Fatal("Allowed(其他 IP) = false, want true")
	}

	// A successful login calls Reset, which restores it at once.
	l.Reset(ip)
	if !l.Allowed(ip) {
		t.Fatal("Allowed(Reset 后) = false, want true")
	}

	// When the window expires it recovers by itself, and the new window counts
	// from zero again.
	for i := 0; i < 5; i++ {
		l.Fail(ip)
	}
	if l.Allowed(ip) {
		t.Fatal("Allowed(再次 5 次失败后) = true, want false")
	}
	now = now.Add(90 * time.Second)
	if !l.Allowed(ip) {
		t.Fatal("Allowed(窗口过期后) = false, want true")
	}
	l.Fail(ip)
	if !l.Allowed(ip) {
		t.Fatal("Allowed(新窗口 1 次失败后) = false, want true")
	}
}
