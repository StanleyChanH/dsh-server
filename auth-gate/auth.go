// The user store (users.json) and the login-failure rate limiter.
//
// users.json is a flat map {"user": "$2a$12$..."}. Check reloads the file when
// its mtime changes, so after -adduser rewrites the file in place the running
// service sees users come and go without a restart.

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the bcrypt cost written to users.json (-adduser uses it to
// generate a hash).
const bcryptCost = 12

// nowFunc lets tests inject the time; production uses time.Now.
var nowFunc = time.Now

// Users keeps an in-memory copy of users.json and reloads it lazily by mtime
// in Check. The zero value works: until LoadUsers, Check is always false.
type Users struct {
	mu      sync.RWMutex
	path    string
	modTime time.Time
	hashes  map[string]string // user name -> bcrypt hash
}

// LoadUsers loads the user table from path at start-up. It returns an error
// when the file cannot be read or is not valid.
func (u *Users) LoadUsers(path string) error {
	hashes, modTime, err := readUsersFile(path)
	if err != nil {
		return err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.path = path
	u.modTime = modTime
	u.hashes = hashes
	return nil
}

// Check verifies a user name and password (bcrypt.CompareHashAndPassword).
// Every call first looks at the mtime of users.json and reloads when it
// changed; a failed reload keeps the old data.
func (u *Users) Check(username, password string) bool {
	u.reloadIfChanged()

	u.mu.RLock()
	hash, ok := u.hashes[username]
	u.mu.RUnlock()
	if !ok {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// Exists reports whether the user is in the current user table (the userExists
// callback used when verifying gate_session: once a user is removed, their
// cookie stops working at once). Like Check, it reloads lazily by mtime.
func (u *Users) Exists(username string) bool {
	u.reloadIfChanged()

	u.mu.RLock()
	defer u.mu.RUnlock()
	_, ok := u.hashes[username]
	return ok
}

// reloadIfChanged reloads the user table when the mtime has changed.
func (u *Users) reloadIfChanged() {
	u.mu.RLock()
	path, loaded := u.path, u.modTime
	u.mu.RUnlock()
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.ModTime().Equal(loaded) {
		return
	}
	hashes, modTime, err := readUsersFile(path)
	if err != nil {
		log.Printf("auth: reload %s: %v（沿用已加载的用户表）", path, err)
		return
	}
	u.mu.Lock()
	u.modTime = modTime
	u.hashes = hashes
	u.mu.Unlock()
}

// readUsersFile reads and parses users.json. It stats before it reads, so the
// returned mtime is never newer than the content that was read: a rewrite in
// between only causes one extra reload the next time, it never misses an
// update.
func readUsersFile(path string) (map[string]string, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	var hashes map[string]string
	if err := json.Unmarshal(data, &hashes); err != nil {
		return nil, time.Time{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return hashes, fi.ModTime(), nil
}

// limitEntry is the failure count of one IP inside the current fixed window.
type limitEntry struct {
	count       int
	windowStart time.Time
}

// Limiter rate-limits login failures per IP with a fixed window: once an IP
// has max failures inside one window, it is refused until the window expires
// or a successful login calls Reset. Only failures count; a success resets the
// counter.
type Limiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	entries   map[string]limitEntry
	lastSweep time.Time
}

// NewLimiter returns a limiter that allows at most max failures per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{
		max:     max,
		window:  window,
		entries: make(map[string]limitEntry),
	}
}

// Fail records one failure; when the window has expired, counting restarts in
// a new window.
func (l *Limiter) Fail(ip string) {
	now := nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	e, ok := l.entries[ip]
	if !ok || now.Sub(e.windowStart) >= l.window {
		e = limitEntry{windowStart: now}
	}
	e.count++
	l.entries[ip] = e
}

// Allowed reports whether the IP may still try (fewer than max failures in
// the current window).
func (l *Limiter) Allowed(ip string) bool {
	now := nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || now.Sub(e.windowStart) >= l.window {
		return true
	}
	return e.count < l.max
}

// Reset clears the failure count of the IP (called after a successful login).
func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// sweepLocked lazily removes the entries whose window has expired, so that
// many one-off IPs cannot grow the map without bound. At most one full sweep
// per window, amortised O(1).
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.window {
		return
	}
	for ip, e := range l.entries {
		if now.Sub(e.windowStart) >= l.window {
			delete(l.entries, ip)
		}
	}
	l.lastSweep = now
}
