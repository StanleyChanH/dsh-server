package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// aliceHash computes the cost-12 hash only once (about 0.25s); every test
// reuses it.
var aliceHash = sync.OnceValue(func() string {
	hash, err := bcrypt.GenerateFromPassword([]byte("alice-password"), bcryptCost)
	if err != nil {
		panic(err)
	}
	return string(hash)
})

// newTestMux wires routes with their own state (user table, limiter, signing
// key) and returns the mux and the Users. Every test gets its own, so failure
// counts do not leak between tests. The session lifetime is the default 720h.
func newTestMux(t *testing.T) (*http.ServeMux, *Users) {
	t.Helper()
	setSecret(t, []byte("test-secret"))
	path := usersPath(t)
	writeUsers(t, path, map[string]string{"alice": aliceHash()})
	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: %v", err)
	}
	return newMux(NewGate(&users, &TokenStore{}, NewLimiter(5, time.Minute), 720)), &users
}

// postLogin POSTs one JSON login from the given client IP (two XFF elements,
// to verify that only the first is used).
func postLogin(t *testing.T, mux *http.ServeMux, ip, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", ip+", 10.0.0.1")
	req.RemoteAddr = "192.0.2.1:1234"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLoginPageServed(t *testing.T) {
	mux, _ := newTestMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/login code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
	if !strings.Contains(rec.Body.String(), "连接到节点") {
		t.Fatal("body 未包含登录页文案「连接到节点」，embed 的 login.html 未被返回")
	}
}

func TestLoginSuccessSetsCookie(t *testing.T) {
	mux, users := newTestMux(t)
	rec := postLogin(t, mux, "203.0.113.7", `{"username":"alice","password":"alice-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确凭证 code = %d, want 200；body %q", rec.Code, rec.Body.String())
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "gate_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("响应未设置 gate_session cookie")
	}
	if !cookie.HttpOnly {
		t.Error("gate_session 缺 HttpOnly")
	}
	if !cookie.Secure {
		t.Error("gate_session 缺 Secure")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("gate_session SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("gate_session Path = %q, want \"/\"（/auth 之外的路径也要带 cookie）", cookie.Path)
	}
	if want := 720 * 3600; cookie.MaxAge != want {
		t.Errorf("gate_session MaxAge = %d, want %d（720h）", cookie.MaxAge, want)
	}
	// The cookie value passes Verify with the real Users.Exists callback and
	// yields the user name.
	if user, err := Verify(cookie.Value, users.Exists); err != nil || user != "alice" {
		t.Errorf("Verify(Set-Cookie 值, users.Exists) = %q, %v; want %q, nil", user, err, "alice")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	mux, _ := newTestMux(t)
	for name, body := range map[string]string{
		"wrong-password": `{"username":"alice","password":"wrong"}`,
		"unknown-user":   `{"username":"bob","password":"alice-password"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postLogin(t, mux, "203.0.113.8", body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("code = %d, want 401", rec.Code)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "凭证不匹配" {
				t.Fatalf("body = %q, want %q", got, "凭证不匹配")
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Error("失败登录不应设置 cookie")
			}
		})
	}

	// Invalid JSON: 400 "request format error" (neither the rate limit nor the
	// credential check is reached).
	rec := postLogin(t, mux, "203.0.113.8", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body code = %d, want 400", rec.Code)
	}
}

// An oversized body (valid JSON, but with more than 4 KB of padding) has to be
// rejected while it is decoded, not streamed into memory: protection against
// remote OOM by an anonymous oversized POST.
func TestLoginBodyTooLarge(t *testing.T) {
	mux, _ := newTestMux(t)
	body := `{"username":"alice","password":"alice-password","pad":"` + strings.Repeat("x", 8192) + `"}`
	rec := postLogin(t, mux, "203.0.113.10", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超限 body code = %d, want 400（当前实现若解码成功会误返回 200）", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("被拒请求不应设置 cookie")
	}
}

func TestRateLimitReturns429(t *testing.T) {
	mux, _ := newTestMux(t)
	const ip = "198.51.100.23"

	// Five failures in a row from the same IP; each one has to be a 401.
	for i := 0; i < 5; i++ {
		rec := postLogin(t, mux, ip, `{"username":"alice","password":"wrong"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败尝试 code = %d, want 401", i+1, rec.Code)
		}
	}
	// The 6th attempt gets a 429 even with the right password, and no cookie.
	rec := postLogin(t, mux, ip, `{"username":"alice","password":"alice-password"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("受限后正确密码 code = %d, want 429", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "尝试过多" {
		t.Fatalf("body = %q, want %q", got, "尝试过多")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("429 响应不应设置 cookie")
	}
	// Another IP is not affected.
	rec = postLogin(t, mux, "198.51.100.24", `{"username":"alice","password":"alice-password"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("其他 IP 正确密码 code = %d, want 200", rec.Code)
	}
}

func TestCheckOkWithCookie(t *testing.T) {
	mux, _ := newTestMux(t)

	t.Run("ValidCookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/check", nil)
		req.AddCookie(&http.Cookie{Name: "gate_session", Value: Sign("alice", time.Now().Add(time.Hour))})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
			t.Fatalf("body = %q, want %q", got, "ok")
		}
	})

	// A signed cookie for a user outside the user table has to fail closed
	// (the userExists callback is Users.Exists).
	// A GET that did not pass the gate -> 302 to the login page (the decision:
	// a browser only navigates on 3xx).
	t.Run("UnknownUserCookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/check", nil)
		req.AddCookie(&http.Cookie{Name: "gate_session", Value: Sign("bob", time.Now().Add(time.Hour))})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("code = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2F" {
			t.Fatalf("Location = %q, want %q", loc, "/auth/login?next=%2F")
		}
	})
}

// Not past the gate: GET -> 302 + Location to the login page (a browser only
// navigates on 3xx); anything else -> 401 + Location (API, XHR and WebSocket
// semantics: a client must not treat a 401 as a page navigation).
func TestCheckUnauthorizedRedirectsToLogin(t *testing.T) {
	mux, _ := newTestMux(t)

	t.Run("ForwardedURI", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/auth/check", nil)
		req.Header.Set("X-Forwarded-Uri", "/x/y?z=1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("code = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2Fx%2Fy%3Fz%3D1" {
			t.Fatalf("Location = %q, want %q", loc, "/auth/login?next=%2Fx%2Fy%3Fz%3D1")
		}
	})

	t.Run("DefaultRoot", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/check", nil))
		if rec.Code != http.StatusFound {
			t.Fatalf("code = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2F" {
			t.Fatalf("Location = %q, want %q", loc, "/auth/login?next=%2F")
		}
	})

	t.Run("TamperedCookie", func(t *testing.T) {
		value := Sign("alice", time.Now().Add(time.Hour))
		last := value[len(value)-1]
		repl := byte('A')
		if last == 'A' {
			repl = 'E'
		}
		req := httptest.NewRequest(http.MethodGet, "/auth/check", nil)
		req.AddCookie(&http.Cookie{Name: "gate_session", Value: value[:len(value)-1] + string(repl)})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("code = %d, want 302", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2F" {
			t.Fatalf("Location = %q, want %q", loc, "/auth/login?next=%2F")
		}
	})

	// A non-GET (a POST without a cookie) keeps 401 + Location: Caddy's
	// forward_auth keeps the method of the original request, so API calls and
	// WebSocket handshakes get a 401 and can report the error properly instead
	// of being treated as a redirect.
	t.Run("NonGetKeeps401", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/auth/check", nil)
		req.Header.Set("X-Forwarded-Uri", "/x/y?z=1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code = %d, want 401", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2Fx%2Fy%3Fz%3D1" {
			t.Fatalf("Location = %q, want %q", loc, "/auth/login?next=%2Fx%2Fy%3Fz%3D1")
		}
	})
}

func TestPing(t *testing.T) {
	mux, _ := newTestMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/ping", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /auth/ping code = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "pong" {
		t.Fatalf("body = %q, want %q", got, "pong")
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9 , 10.0.0.1") // several elements, with a space
	if got := clientIP(req); got != "203.0.113.9" {
		t.Errorf("clientIP(XFF 多段) = %q, want 第一段 %q", got, "203.0.113.9")
	}

	req = httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	if got := clientIP(req); got != "192.0.2.1" {
		t.Errorf("clientIP(无 XFF) = %q, want RemoteAddr host %q", got, "192.0.2.1")
	}

	// Present but empty: what caddy forwards for a request on the tunnel
	// listener that carries no Cf-Connecting-Ip (caddy/Caddyfile, :8080).
	req = httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Forwarded-For", "")
	if got := clientIP(req); got != "192.0.2.1" {
		t.Errorf("clientIP(空 XFF) = %q, want RemoteAddr host %q", got, "192.0.2.1")
	}
}

// ---- Automatic sign-in on 401: /auth/dsh-entry (increment of 2026-11-22) ----

// newDshEntryMux wires routes with their own state (user table + TokenStore).
// tokenPath goes to LoadToken (the file may be absent: the start-up state,
// before the host sync script has written it, is a valid state); the caller
// writes the token file's content.
func newDshEntryMux(t *testing.T, tokenPath string) *http.ServeMux {
	t.Helper()
	setSecret(t, []byte("test-secret"))
	path := usersPath(t)
	writeUsers(t, path, map[string]string{"alice": aliceHash()})
	var users Users
	if err := users.LoadUsers(path); err != nil {
		t.Fatalf("LoadUsers: %v", err)
	}
	var tokens TokenStore
	if err := tokens.LoadToken(tokenPath); err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	return newMux(NewGate(&users, &tokens, NewLimiter(5, time.Minute), 720))
}

// dshEntryReq builds a request with a valid alice gate_session.
func dshEntryReq(method, target string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: Sign("alice", time.Now().Add(time.Hour))})
	return req
}

// writeTokenFile writes the token file (the shape the host script writes: the
// token string only, a trailing newline allowed).
func writeTokenFile(t *testing.T, path, token string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("写 token 文件 %s: %v", path, err)
	}
}

func TestDshEntryRedirectsWithToken(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "dsh-token")
	writeTokenFile(t, tokenPath, "tok123")
	mux := newDshEntryMux(t, tokenPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, dshEntryReq(http.MethodGet, "/auth/dsh-entry"))
	if rec.Code != http.StatusFound {
		t.Fatalf("有效 gate cookie + token 就绪：code = %d, want 302；body %q", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=tok123" {
		t.Fatalf("Location = %q, want %q", loc, "/?token=tok123")
	}

	var retry *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "gate_entry_retry" {
			retry = c
		}
	}
	if retry == nil {
		t.Fatal("响应未设置 gate_entry_retry cookie")
	}
	if retry.MaxAge != 35 {
		t.Errorf("gate_entry_retry MaxAge = %d, want 35（略长于 30s 窗口）", retry.MaxAge)
	}
	if !retry.HttpOnly {
		t.Error("gate_entry_retry 缺 HttpOnly")
	}
	if !retry.Secure {
		t.Error("gate_entry_retry 缺 Secure")
	}
	if retry.SameSite != http.SameSiteLaxMode {
		t.Errorf("gate_entry_retry SameSite = %v, want Lax", retry.SameSite)
	}
	if retry.Path != "/" {
		t.Errorf("gate_entry_retry Path = %q, want \"/\"", retry.Path)
	}
	// The value is the unix second at which it was issued.
	setAt, err := strconv.ParseInt(retry.Value, 10, 64)
	if err != nil || time.Now().Unix()-setAt < 0 || time.Now().Unix()-setAt > 5 {
		t.Errorf("gate_entry_retry 值 = %q（err %v），want 当前时刻 unix 秒", retry.Value, err)
	}

	// The core scenario of a dsh restart with a new token: after the host
	// script rewrites the token file (Chtimes pins the mtime change, to get
	// around the timestamp granularity of the file system), the running
	// service has to hand out the new token without a restart. The endpoint
	// takes any method as well: a non-GET request that Caddy intercepted and
	// sent here has to be signed in too.
	writeTokenFile(t, tokenPath, "tok456")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(tokenPath, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, dshEntryReq(http.MethodPost, "/auth/dsh-entry"))
	if rec.Code != http.StatusFound {
		t.Fatalf("token 文件改写后：code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=tok456" {
		t.Fatalf("token 文件改写后：Location = %q, want %q（mtime 自动重载未生效）", loc, "/?token=tok456")
	}
}

func TestDshEntryRequiresGateCookie(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "dsh-token")
	writeTokenFile(t, tokenPath, "tok123")
	mux := newDshEntryMux(t, tokenPath)

	// No cookie at all -> 302 to the login page; the token is never leaked to
	// a request that did not pass the gate.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/dsh-entry", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("无 cookie：code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2F" {
		t.Fatalf("无 cookie：Location = %q, want %q", loc, "/auth/login?next=%2F")
	}
	if strings.Contains(rec.Body.String(), "tok123") {
		t.Fatal("无 cookie：应答体泄露了 token")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("无 cookie：不应下发任何 cookie")
	}

	// A signed cookie for a user outside the user table fails closed as well
	// (the userExists callback of Verify is Users.Exists).
	req := httptest.NewRequest(http.MethodGet, "/auth/dsh-entry", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: Sign("bob", time.Now().Add(time.Hour))})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("未知用户 cookie：code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/auth/login?next=%2F" {
		t.Fatalf("未知用户 cookie：Location = %q, want %q", loc, "/auth/login?next=%2F")
	}
	if strings.Contains(rec.Body.String(), "tok123") {
		t.Fatal("未知用户 cookie：应答体泄露了 token")
	}
}

func TestDshEntryRetryGuard(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "dsh-token")
	writeTokenFile(t, tokenPath, "tok123")
	mux := newDshEntryMux(t, tokenPath)

	// A retry cookie from 5 seconds ago plus a valid gate cookie: the last
	// sign-in just happened and did not get the user in (dsh restarted with a
	// new token that is not synced yet). The sync page has to be served, not
	// another 302: the key to stopping the loop.
	req := dshEntryReq(http.MethodGet, "/auth/dsh-entry")
	req.AddCookie(&http.Cookie{Name: "gate_entry_retry", Value: strconv.FormatInt(time.Now().Add(-5*time.Second).Unix(), 10)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("窗口内重试：code = %d, want 200（同步页）", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("窗口内重试：Location = %q, want 空（不得再 302）", loc)
	}
	if !strings.Contains(rec.Body.String(), "凭证同步中") {
		t.Fatal("窗口内重试：body 缺「凭证同步中」文案")
	}

	// Once the retry cookie (31 seconds old) is outside the 30s window, another
	// sign-in is allowed (the cookie's MaxAge of 35s is deliberately longer
	// than the window: the old cookie is still there but past the window, so
	// handing out a token is allowed again).
	req = dshEntryReq(http.MethodGet, "/auth/dsh-entry")
	req.AddCookie(&http.Cookie{Name: "gate_entry_retry", Value: strconv.FormatInt(time.Now().Add(-31*time.Second).Unix(), 10)})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("窗口外重试：code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=tok123" {
		t.Fatalf("窗口外重试：Location = %q, want %q", loc, "/?token=tok123")
	}
}

func TestDshEntryNoTokenFile(t *testing.T) {
	// Start-up state: the host sync script has not written the token file yet
	// (first deploy, or dsh has just restarted).
	tokenPath := filepath.Join(t.TempDir(), "dsh-token")
	mux := newDshEntryMux(t, tokenPath)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, dshEntryReq(http.MethodGet, "/auth/dsh-entry"))
	if rec.Code != http.StatusOK {
		t.Fatalf("token 文件缺失：code = %d, want 200（同步页；不得 302 到空 token 的 /?token=）", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("token 文件缺失：Location = %q, want 空", loc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("同步页 Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
	if !strings.Contains(rec.Body.String(), "凭证同步中，约 1 分钟后自动恢复") {
		t.Fatal("同步页缺 brief 钉死的文案「凭证同步中，约 1 分钟后自动恢复」")
	}
	if n := rec.Body.Len(); n >= 1<<10 {
		t.Errorf("同步页 %d 字节，want <1KB", n)
	}

	// Within a minute the host script delivers the token file: the same
	// instance, without a restart, hands it out on the next request (the mtime
	// goes from the zero value to a real one -> automatic reload).
	writeTokenFile(t, tokenPath, "tok123")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, dshEntryReq(http.MethodGet, "/auth/dsh-entry"))
	if rec.Code != http.StatusFound {
		t.Fatalf("token 文件补上后：code = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/?token=tok123" {
		t.Fatalf("token 文件补上后：Location = %q, want %q（缺失态记录的路径未触发自动重载）", loc, "/?token=tok123")
	}
}

// A file that exists but is empty (0 bytes) means the same as a missing one:
// Current() normalises it to "", dsh-entry serves the sync page, and it must
// never 302 to /?token= with an empty token (that would bounce the member
// back into a 401 loop).
func TestDshEntryEmptyTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "dsh-token")
	if err := os.WriteFile(tokenPath, nil, 0o600); err != nil {
		t.Fatalf("写空 token 文件 %s: %v", tokenPath, err)
	}
	var tokens TokenStore
	if err := tokens.LoadToken(tokenPath); err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got := tokens.Current(); got != "" {
		t.Fatalf("空文件 Current() = %q, want \"\"", got)
	}

	mux := newDshEntryMux(t, tokenPath)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, dshEntryReq(http.MethodGet, "/auth/dsh-entry"))
	if rec.Code != http.StatusOK {
		t.Fatalf("token 文件为空：code = %d, want 200（同步页）", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("token 文件为空：Location = %q, want 空", loc)
	}
	if !strings.Contains(rec.Body.String(), "凭证同步中") {
		t.Fatal("token 文件为空：body 缺「凭证同步中」文案")
	}
}
