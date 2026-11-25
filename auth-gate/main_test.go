package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAddUserWritesFile covers the file write-back of gate -adduser: a new
// user can log in, the users that were already there are kept, and changing
// an existing user's password in place makes the old password dead at once.
func TestAddUserWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	writeUsers(t, path, map[string]string{"bob": hashPw(t, "bob-password")})

	if err := addUser(path, "alice", "alice-password"); err != nil {
		t.Fatalf("addUser(alice): unexpected error: %v", err)
	}

	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: unexpected error: %v", err)
	}
	if !users.Check("alice", "alice-password") {
		t.Fatal(`addUser 后 Check(alice, "alice-password") = false, want true`)
	}
	if !users.Check("bob", "bob-password") {
		t.Fatal(`addUser 后 Check(bob, "bob-password") = false, want true（原有用户丢失）`)
	}

	// In place: running adduser again with the same name changes the password,
	// and the old hash has to be replaced as a whole.
	if err := addUser(path, "alice", "new-password"); err != nil {
		t.Fatalf("addUser(alice 更新): unexpected error: %v", err)
	}
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers(更新后): unexpected error: %v", err)
	}
	if !users.Check("alice", "new-password") {
		t.Fatal(`更新后 Check(alice, "new-password") = false, want true`)
	}
	if users.Check("alice", "alice-password") {
		t.Fatal(`更新后 Check(alice, "alice-password") = true, want false（旧密码仍有效）`)
	}
}

// TestNewServerTimeouts: the listening server has to carry read-side timeout
// guard rails. ReadHeaderTimeout deals with slow-header (slowloris-style) DoS,
// and ReadTimeout puts an upper bound on request bodies. WriteTimeout has to
// stay 0: once set, it would cut off future long-lived and SSE-style streaming
// answers.
func TestNewServerTimeouts(t *testing.T) {
	srv := newServer(":8080", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0（不设：长连接/SSE 会被写超时切断）", srv.WriteTimeout)
	}
}

// TestLoadSessionSecretMinLength: a session-signing key that decodes to fewer
// than 32 bytes has to refuse to start (fail fast). A short key shrinks the
// brute-force space of the HMAC dramatically; an empty value or invalid hex is
// rejected as well.
func TestLoadSessionSecretMinLength(t *testing.T) {
	t.Run("32字节通过", func(t *testing.T) {
		t.Setenv("AUTH_GATE_SECRET", strings.Repeat("ab", 32))
		secret, err := loadSessionSecret()
		if err != nil || len(secret) != 32 {
			t.Fatalf("loadSessionSecret = %d 字节, %v; want 32, nil", len(secret), err)
		}
	})
	for name, val := range map[string]string{
		"空":    "",
		"非hex": "zzzz",
		"2字节":  "abcd",
		"31字节": strings.Repeat("ab", 31),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AUTH_GATE_SECRET", val)
			if _, err := loadSessionSecret(); err == nil {
				t.Fatalf("loadSessionSecret(%s) 应报错，得到 nil", name)
			}
		})
	}
}

// TestRunAddUserRejectsInvalidNames: a user name outside the whitelist
// ^[A-Za-z0-9._-]{1,32}$ has to be rejected before the password is read (fail
// fast), and users.json must not be written. A name containing "|" would break
// the user|unix separator format of the gate_session payload; whitespace,
// non-ASCII and names that are too long are rejected along with it.
func TestRunAddUserRejectsInvalidNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	t.Setenv("AUTH_USERS", path)
	for _, name := range []string{
		"", "a|b", "with space", "a:b", "a/b", "用户", "a\nb", strings.Repeat("a", 33),
	} {
		if err := runAddUser(name, strings.NewReader("some-password\n")); err == nil {
			t.Errorf("runAddUser(%q) = nil, want 报错", name)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("非法用户名不应写 users.json：stat err = %v, want 不存在", err)
	}
}

// TestRunAddUserAcceptsValidNames: names inside the whitelist (including "."
// and the 32-character limit) go through the whole chain (read the password
// from stdin -> bcrypt -> write the file -> can log in).
func TestRunAddUserAcceptsValidNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	t.Setenv("AUTH_USERS", path)
	for _, name := range []string{"alice", "john.doe", "a._-b", strings.Repeat("a", 32)} {
		if err := runAddUser(name, strings.NewReader("some-password\n")); err != nil {
			t.Errorf("runAddUser(%q) 意外报错: %v", name, err)
		}
	}
	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: %v", err)
	}
	if !users.Check("john.doe", "some-password") {
		t.Error(`Check("john.doe", "some-password") = false, want true`)
	}
}

// TestAddUserCreatesFile verifies that -adduser creates users.json itself
// when it does not exist yet (first deploy).
func TestAddUserCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "users.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := addUser(path, "alice", "alice-password"); err != nil {
		t.Fatalf("addUser: unexpected error: %v", err)
	}

	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: unexpected error: %v", err)
	}
	if !users.Check("alice", "alice-password") {
		t.Fatal(`Check(alice, "alice-password") = false, want true`)
	}
}
