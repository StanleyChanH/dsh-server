// HTTP handlers: the login page, the login submission, the forward_auth check
// and the health probe.
//
// Caddy sends protected paths through forward_auth -> /auth/check. When the
// cookie is invalid, GET gets a 302 and every other method a 401, both with
// Location: /auth/login?next=<original path>: a browser only navigates on 3xx,
// so the 302 sends page requests to the login page, while the 401 keeps the
// API semantics. After a successful login the front end follows next back to
// where it came from.

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed login.html
var loginHTML []byte

// Name of the session cookie. Caddy passes /auth/* straight to this service;
// every other path is let through on it.
const sessionCookie = "gate_session"

// Gate collects what the handlers depend on: the user table, the token store,
// the failure limiter and the session lifetime in hours.
type Gate struct {
	users        *Users
	tokens       *TokenStore
	limiter      *Limiter
	sessionHours int
}

// NewGate wires a Gate (users, tokens and limiter are built by main and
// injected).
func NewGate(users *Users, tokens *TokenStore, limiter *Limiter, sessionHours int) *Gate {
	return &Gate{users: users, tokens: tokens, limiter: limiter, sessionHours: sessionHours}
}

// loginPage serves GET /auth/login: the embedded login page.
func (g *Gate) loginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(loginHTML)
}

// loginBodyLimit is the upper bound for the body of a login request: the
// credentials are two short strings, so 4 KB is plenty. Anything larger is
// rejected, so an anonymous oversized request cannot use the decoder as an
// unbounded buffer.
const loginBodyLimit = 1 << 12

// loginSubmit handles POST /auth/login: JSON {username,password}.
// Order: cap the body -> rate limit (a refusal says nothing about whether the
// credentials were right) -> check the credentials -> on success, reset the
// counter and issue the cookie.
// Each of the three outcomes (success, fail, ratelimited) writes one audit
// log line with the user name, the client IP and the result (`docker compose
// logs auth-gate` shows brute-force attempts and helps with troubleshooting).
// The password is never logged.
func (g *Gate) loginSubmit(w http.ResponseWriter, r *http.Request) {
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, loginBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		http.Error(w, "请求格式错误", http.StatusBadRequest)
		return
	}

	ip := clientIP(r)
	if !g.limiter.Allowed(ip) {
		log.Printf("auth: login user=%q ip=%s result=ratelimited", cred.Username, ip)
		http.Error(w, "尝试过多", http.StatusTooManyRequests)
		return
	}
	if !g.users.Check(cred.Username, cred.Password) {
		g.limiter.Fail(ip)
		log.Printf("auth: login user=%q ip=%s result=fail", cred.Username, ip)
		http.Error(w, "凭证不匹配", http.StatusUnauthorized)
		return
	}

	log.Printf("auth: login user=%q ip=%s result=success", cred.Username, ip)
	g.limiter.Reset(ip)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    Sign(cred.Username, nowFunc().Add(time.Duration(g.sessionHours)*time.Hour)),
		Path:     "/",
		MaxAge:   g.sessionHours * 3600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ok":true}`)
}

// check serves /auth/check (the target of Caddy's forward_auth, any method,
// because forward_auth keeps the method of the original request). A valid
// cookie gets 200 "ok"; otherwise the refusal code depends on the method:
// GET -> 302 (a browser only navigates on 3xx; a 401 would just render a
// blank page), anything else -> 401 (API, XHR and WebSocket semantics: a
// client must not treat a 401 as a redirect).
// Both carry Location: /auth/login?next=<original path> to come back to after
// the login.
func (g *Gate) check(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, verr := Verify(c.Value, g.users.Exists); verr == nil {
			w.Write([]byte("ok"))
			return
		}
	}
	w.Header().Set("Location", "/auth/login?next="+url.QueryEscape(nextURI(r.Header.Get("X-Forwarded-Uri"))))
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// ping serves GET /auth/ping: used by the container healthcheck, depends on
// no state.
func (g *Gate) ping(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("pong"))
}

// TokenStore keeps an in-memory copy of the dsh-token file (written on the
// host by sync-token.sh, containing the token string only) and reloads it
// lazily by mtime, the same pattern as Users: after dsh restarts with a new
// token, the new value is served without a restart. The zero value works:
// until LoadToken, Current is always "".
type TokenStore struct {
	mu      sync.RWMutex
	path    string
	modTime time.Time
	token   string
}

// LoadToken records the path and does the initial read. A missing file is not
// an error (a token that is not synced yet is a valid state; cron fills it in
// every minute): the path is recorded, the token stays empty, and once the
// file appears the reload by mtime picks it up. Any other read error is
// returned for the caller to log, but the path is still recorded, so later
// mtime changes reload as usual.
func (ts *TokenStore) LoadToken(path string) error {
	token, modTime, err := readTokenFile(path)
	ts.mu.Lock()
	ts.path = path
	ts.modTime = modTime
	ts.token = token
	ts.mu.Unlock()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Current returns the current token, reloading first when the mtime changed;
// a missing or empty file gives "". It never fails: the handler treats "" as
// "not ready" and serves the sync page.
func (ts *TokenStore) Current() string {
	ts.reloadIfChanged()

	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.token
}

// reloadIfChanged rereads the token file when the mtime changed. A failed
// reload keeps the old value (the same when the file is missing: while a
// degraded-mode drill moves the file away, the old token keeps being handed
// out, and the retry cookie limits the rate).
func (ts *TokenStore) reloadIfChanged() {
	ts.mu.RLock()
	path, loaded := ts.path, ts.modTime
	ts.mu.RUnlock()
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || fi.ModTime().Equal(loaded) {
		return
	}
	token, modTime, err := readTokenFile(path)
	if err != nil {
		log.Printf("auth: reload %s: %v（沿用已加载的 token）", path, err)
		return
	}
	ts.mu.Lock()
	ts.modTime = modTime
	ts.token = token
	ts.mu.Unlock()
}

// readTokenFile reads and normalises the token file: stat before read (for
// the same reason as in readUsersFile, the returned mtime is never newer than
// the content that was read), and TrimSpace removes the trailing newline the
// script writes.
func readTokenFile(path string) (string, time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), fi.ModTime(), nil
}

// dshEntrySyncHTML is the minimal "syncing credentials" page (a const string,
// under 1 KB, no external links, dark background, light text). It is served
// when the token file is not ready yet, or when a sign-in just happened. After
// 20 seconds a meta refresh goes through the front door "/" again (by then
// the retry cookie has mostly expired and cron has mostly delivered the
// token), which makes the "recovers by itself" in the text true.
const dshEntrySyncHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="20;url=/">
<title>凭证同步中</title>
<style>html{background:#111;color:#eee;font-family:system-ui,sans-serif}body{display:grid;place-items:center;min-height:100vh;margin:0}p{padding:0 1em;text-align:center;line-height:1.8}</style>
</head>
<body><p>凭证同步中，约 1 分钟后自动恢复。<br>本页会自动刷新，无需操作。</p></body>
</html>`

// Loop guard for automatic sign-in: handing out the token sets
// gate_entry_retry (value = unix seconds of that moment). Arriving again
// within entryRetryWindow means the last sign-in did not get the user in (dsh
// restarted with a new token that is not synced yet), so the sync page is
// served instead of another 302.
// The cookie's MaxAge (35s) is deliberately longer than the window (30s): the
// guard records the fact that a sign-in happened at that moment, and once the
// window has passed another one is allowed even if the old cookie is still
// there.
const (
	entryRetryCookie = "gate_entry_retry"
	entryRetryWindow = 30 * time.Second
	entryRetryMaxAge = 35
)

// dshEntry serves /auth/dsh-entry (any method: Caddy turns every 401 from dsh
// into a 302 to here, so non-GET requests arrive as well): a member holding a
// valid gate cookie gets a 302 that carries the current dsh token, which
// makes the gate account the only credential. Branches: no valid gate cookie
// -> 302 to the login page; token not ready -> sync page; retry inside the 30s
// window -> sync page; otherwise -> 302 /?token=<current>.
func (g *Gate) dshEntry(w http.ResponseWriter, r *http.Request) {
	valid := false
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, verr := Verify(c.Value, g.users.Exists); verr == nil {
			valid = true
		}
	}
	if !valid {
		// Never hand out a token to a request that did not pass the gate
		// (next is pinned to /: this endpoint does not know the member's
		// original path).
		w.Header().Set("Location", "/auth/login?next="+url.QueryEscape("/"))
		w.WriteHeader(http.StatusFound)
		return
	}

	syncPage := func() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(dshEntrySyncHTML))
	}

	token := g.tokens.Current()
	if token == "" {
		syncPage() // file missing or empty: never a 302 to /?token= with an empty token
		return
	}
	if c, err := r.Cookie(entryRetryCookie); err == nil {
		if last, perr := strconv.ParseInt(c.Value, 10, 64); perr == nil && nowFunc().Sub(time.Unix(last, 0)) < entryRetryWindow {
			syncPage() // a sign-in just happened: wait for the sync instead of looping
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     entryRetryCookie,
		Value:    strconv.FormatInt(nowFunc().Unix(), 10),
		Path:     "/",
		MaxAge:   entryRetryMaxAge,
		HttpOnly: true,
		Secure:   true, // TLS only in this stack, same as gate_session
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Location", "/?token="+url.QueryEscape(token))
	w.WriteHeader(http.StatusFound)
}

// nextURI takes the path and query of X-Forwarded-Uri, or "/" when it is
// absent. It is normalised through url.Parse: even if the reverse proxy passes
// a full URI, only the path part is kept, so an open redirect is impossible.
func nextURI(v string) string {
	if v == "" {
		return "/"
	}
	if u, err := url.Parse(v); err == nil && u.Path != "" {
		if u.RawQuery != "" {
			return u.Path + "?" + u.RawQuery
		}
		return u.Path
	}
	return v
}

// clientIP returns the client IP: the first element of X-Forwarded-For
// (Caddy appends to it on the trusted internal chain), or the host part of
// RemoteAddr when there is no XFF.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
