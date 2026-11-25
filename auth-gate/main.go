// auth-gate entry point: reads the environment, wires the routes and listens.
//
// Environment variables: AUTH_GATE_SECRET (hex session-signing key; invalid
// hex, or fewer than 32 bytes after decoding, both refuse to start),
// AUTH_USERS (path of users.json), AUTH_SESSION_HOURS (cookie lifetime),
// AUTH_ADDR.

package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// usernameRe is the whitelist for user names: letters, digits, `.`, `_`, `-`,
// 1 to 32 characters. Typical rejects: `|` (it separates user name and unix
// seconds in the gate_session payload, so one inside a name would break
// signing and verification), whitespace and non-ASCII (escaping surfaces in
// shells, URLs and jq), and names that are too long (bloat in logs and
// indexes).
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// Login-failure rate limiting: at most 5 failures per IP per window.
const (
	rateLimitMax    = 5
	rateLimitWindow = time.Minute
)

// newMux attaches the Gate's handlers to a standard ServeMux (with the method
// patterns of Go 1.22+). /auth/check and /auth/dsh-entry accept any method:
// Caddy's forward_auth and handle_response keep, or rewrite to, the method of
// the original request (a POST reaches the check endpoint too, and the 401
// interception sends non-GET dsh requests to sign-in as well). The other
// endpoints are limited to the method they are meant for.
func newMux(g *Gate) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", g.loginPage)
	mux.HandleFunc("POST /auth/login", g.loginSubmit)
	mux.HandleFunc("/auth/check", g.check)
	mux.HandleFunc("/auth/dsh-entry", g.dshEntry)
	mux.HandleFunc("GET /auth/ping", g.ping)
	return mux
}

func main() {
	adduserName := flag.String("adduser", "", "用户名：从 stdin 读一行明文密码，bcrypt 后写入/更新 users.json")
	ping := flag.Bool("ping", false, "GET /auth/ping 探活：应答 pong 退出码 0，否则 1")
	flag.Parse()

	// Subcommand mode: with any flag set, the service does not start (neither
	// subcommand needs AUTH_GATE_SECRET, and adduser often runs on a first
	// deploy before there is a key). flag.NFlag rather than a value check, so
	// that `-adduser ""` is reported as a bad argument instead of starting the
	// service.
	if flag.NFlag() > 0 {
		if *ping {
			if err := runPing(); err != nil {
				log.Fatal(err)
			}
			return
		}
		if err := runAddUser(*adduserName, os.Stdin); err != nil {
			log.Fatal(err)
		}
		return
	}

	secret, err := loadSessionSecret()
	if err != nil {
		log.Fatal(err)
	}
	SessionSecret = secret

	usersPath := envDefault("AUTH_USERS", "/data/users.json")
	var users Users
	if err := users.LoadUsers(usersPath); err != nil {
		log.Fatalf("加载用户表 %s：%v", usersPath, err)
	}

	sessionHours := 720
	if v := os.Getenv("AUTH_SESSION_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Fatalf("AUTH_SESSION_HOURS 需为正整数，得到 %q", v)
		}
		sessionHours = n
	}

	// The dsh token file: written next to users.json by scripts/sync-token.sh
	// on the host (cron, every minute). A missing file is not an error: until
	// the first sync, and in the window right after a dsh restart, the
	// "syncing credentials" page covers for it. Once the file appears it is
	// reloaded by mtime, without a restart.
	tokenPath := filepath.Join(filepath.Dir(usersPath), "dsh-token")
	var tokens TokenStore
	if err := tokens.LoadToken(tokenPath); err != nil {
		log.Printf("auth: 初次加载 token %s 失败：%v（同步窗口期由同步页兜底）", tokenPath, err)
	}

	addr := envDefault("AUTH_ADDR", ":8080")
	log.Printf("auth-gate 监听 %s（用户表 %s，会话 %dh，token 文件 %s）", addr, usersPath, sessionHours, tokenPath)
	log.Fatal(newServer(addr, newMux(NewGate(&users, &tokens, NewLimiter(rateLimitMax, rateLimitWindow), sessionHours))).ListenAndServe())
}

// loadSessionSecret reads the session-signing key from AUTH_GATE_SECRET: hex,
// and at least 32 bytes after decoding. A short key shrinks the brute-force
// space of the HMAC dramatically, so the service refuses to start (fail
// fast); an empty value or invalid hex is rejected as well. The output of
// openssl rand -hex 32 (64 hex characters) is exactly 32 bytes.
func loadSessionSecret() ([]byte, error) {
	secret, err := hex.DecodeString(os.Getenv("AUTH_GATE_SECRET"))
	if err != nil {
		return nil, fmt.Errorf("AUTH_GATE_SECRET 不是合法 hex：%w", err)
	}
	if len(secret) < 32 {
		return nil, fmt.Errorf("AUTH_GATE_SECRET 解码后仅 %d 字节，至少 32 字节（用 openssl rand -hex 32 的输出）", len(secret))
	}
	return secret, nil
}

// newServer returns an http.Server with timeout guard rails (instead of a
// bare http.ListenAndServe): ReadHeaderTimeout against slow headers
// (slowloris-style DoS), ReadTimeout as an upper bound for request bodies.
// WriteTimeout deliberately stays 0: once set, it cuts off slow responses over
// the whole life of the request, which would sever long-lived and SSE-style
// streaming answers. This service has no slow-write abuse surface that would
// need a write timeout.
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
}

// envDefault returns the environment variable, or the default when it is
// empty.
func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// runAddUser implements gate -adduser <name>: it reads one line of plaintext
// password from r, bcrypts it, and writes it to users.json. The password is
// neither logged nor echoed. The user name is checked against the whitelist
// (usernameRe) before the password is read: an invalid name fails fast without
// consuming stdin or touching the file.
func runAddUser(name string, r io.Reader) error {
	if name == "" {
		return errors.New("-adduser 需要用户名：gate -adduser <name>，密码从 stdin 读一行明文")
	}
	if !usernameRe.MatchString(name) {
		return fmt.Errorf("非法用户名 %q：仅限字母/数字/./_/-，长度 1–32（| 与空白等会破坏 cookie 载荷/shell 转义）", name)
	}
	password, err := readPassword(r)
	if err != nil {
		return err
	}
	path := envDefault("AUTH_USERS", "/data/users.json")
	if err := addUser(path, name, password); err != nil {
		return err
	}
	fmt.Printf("user %s updated\n", name)
	return nil
}

// readPassword reads one line from r (without a trailing \n or \r) as the
// plaintext password; an empty password is an error. It does not truncate: a
// password that is too long is reported by bcrypt, not quietly swapped for a
// different one.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("读密码：%w", err)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("密码为空（stdin 需要一行非空明文）")
	}
	return password, nil
}

// addUser writes name -> bcrypt(plaintext) into users.json at path (a flat
// map). An existing file is read in and merged first, so the users already in
// it are kept. The write goes through a temporary file in the same directory
// plus a rename, an atomic replacement, so the running service, which reloads
// by mtime, never reads a half-written file. The temporary file is created
// 0600, which makes users.json 0600 after the rename. A missing file is
// created from an empty table.
func addUser(path, name, password string) error {
	hashes := map[string]string{}
	switch data, err := os.ReadFile(path); {
	case err == nil:
		if err := json.Unmarshal(data, &hashes); err != nil {
			return fmt.Errorf("解析 %s：%w", path, err)
		}
	case errors.Is(err, fs.ErrNotExist):
		// First creation: start from an empty table.
	default:
		return fmt.Errorf("读取 %s：%w", path, err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("bcrypt 哈希用户 %s 的密码：%w", name, err)
	}
	hashes[name] = string(hash)

	data, err := json.Marshal(hashes)
	if err != nil {
		return fmt.Errorf("序列化用户表：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".users.json-*")
	if err != nil {
		return fmt.Errorf("在 %s 建临时文件：%w", filepath.Dir(path), err)
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写临时文件：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件：%w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("用临时文件替换 %s：%w", path, err)
	}
	return nil
}

// runPing implements gate -ping: GET http://127.0.0.1<AUTH_ADDR>/auth/ping,
// and nil (exit code 0) when the body is pong, otherwise an error (log.Fatal,
// exit code 1).
func runPing() error {
	addr := envDefault("AUTH_ADDR", ":8080")
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	url := "http://" + host + "/auth/ping"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("ping %s：%w", url, err)
	}
	defer resp.Body.Close()
	// The ping answer is 4 bytes; reading one byte more tells an oversized
	// answer apart.
	const maxPingBody = int64(len("pong") + 1)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPingBody))
	if err != nil {
		return fmt.Errorf("ping %s 读应答体：%w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping %s：状态码 %d，want 200", url, resp.StatusCode)
	}
	if string(body) != "pong" {
		return fmt.Errorf("ping %s：应答体 %q，want %q", url, body, "pong")
	}
	return nil
}
