package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"flashlarry-api/internal/siteconfig"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

func TestEmailModeNormalizationAndChallengeBinding(t *testing.T) {
	if emailForMode("Swag@example.org", "email") != "swag@example.org" || emailForMode("swag@flashlarry.test", "email") != "" || normalizedTestEmail("swag@flashlarry.test") == "" {
		t.Fatal("email validation")
	}
	a := siteconfig.Config{AuthMode: "email", AuthRevision: 1}
	b := a
	b.AuthRevision = 2
	if challengeKey(a, "swag@example.org") == challengeKey(b, "swag@example.org") {
		t.Fatal("codes survive mode revision")
	}
}
func TestIntegrationEmailModes(t *testing.T) {
	dsn := os.Getenv("FLASHLARRY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	if !strings.Contains(dsn, "flashlarry_integration") {
		t.Fatal("refusing production DB")
	}
	t.Setenv("SETTINGS_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(siteconfig.Schema); e != nil {
		t.Fatal(e)
	}
	cache := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", DB: 14})
	defer cache.Close()
	app := &App{db: db, cache: cache}
	if e := app.initPremium(); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	// Trust only the test SMTP certificate; production code still validates TLS.
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	caPath := filepath.Join(t.TempDir(), "smtp-ca.pem")
	os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600)
	t.Setenv("SSL_CERT_FILE", caPath)
	listener, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	messages := make(chan string, 10)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 test SMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-test\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(line, "AUTH"):
						fmt.Fprint(conn, "235 authenticated\r\n")
					case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "DATA"):
						fmt.Fprint(conn, "354 data\r\n")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						messages <- b.String()
						fmt.Fprint(conn, "250 queued\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "250 OK\r\n")
					}
				}
			}()
		}
	}()
	c, e := siteconfig.Load(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	c.AuthMode = "email"
	c.SMTP = siteconfig.SMTP{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, TLS: "tls", From: "noreply@example.org", Username: "test", Password: "smtp-test-secret"}
	if _, e = siteconfig.Save(ctx, db, c, "integration"); e != nil {
		t.Fatal(e)
	}
	current, e := siteconfig.Load(ctx, db)
	if e != nil {
		t.Fatal(e)
	}
	secret, e := current.SMTP.Secret()
	if e != nil || secret != "smtp-test-secret" {
		t.Fatal("secret encryption")
	}
	public, _ := json.Marshal(current.Public())
	if strings.Contains(string(public), "smtp-test-secret") || strings.Contains(string(public), "password_encrypted") {
		t.Fatal("SMTP secret disclosed")
	}
	call := func(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/test", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Client-IP", "integration-email")
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	email := "flow" + strconv.FormatInt(time.Now().UnixNano(), 10) + "@example.org"
	start := call(app.startEmail, `{"email":"`+email+`"}`)
	if start.Code != 200 {
		t.Fatal(start.Code, start.Body.String())
	}
	message := <-messages
	match := regexp.MustCompile(`Код входа FlashLarry: ([0-9]{6})`).FindStringSubmatch(message)
	if len(match) != 2 {
		t.Fatal("code not delivered over SMTP")
	}
	code := match[1]
	wrong := "111111"
	if wrong == code {
		wrong = "222222"
	}
	if w := call(app.verifyEmail, `{"email":"`+email+`","code":"`+wrong+`"}`); w.Code != 401 {
		t.Fatal("wrong code accepted")
	}
	verify := call(app.verifyEmail, `{"email":"`+email+`","code":"`+code+`"}`)
	if verify.Code != 200 {
		t.Fatal(verify.Code, verify.Body.String())
	}
	if w := call(app.verifyEmail, `{"email":"`+email+`","code":"`+code+`"}`); w.Code != 401 {
		t.Fatal("code replay accepted")
	}
	var auth struct {
		Token string `json:"token"`
		User  User   `json:"user"`
	}
	json.Unmarshal(verify.Body.Bytes(), &auth)
	if auth.User.ID == "" || auth.Token == "" {
		t.Fatal("registration/session missing")
	}
	telegram := app.telegramOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("Telegram operation executed in email mode") }))
	w := httptest.NewRecorder()
	telegram.ServeHTTP(w, httptest.NewRequest("POST", "/auth/telegram/premium", nil))
	if w.Code != 403 {
		t.Fatal("Telegram premium not disabled")
	}
	cfg, _ := siteconfig.Load(ctx, db)
	oldKey := challengeKey(cfg, email)
	hash, _ := emailCodeHash(email, "123456")
	cache.Set(ctx, oldKey, hash, time.Minute)
	cfg.AuthMode = "telegram"
	cfg.SMTP.Password = ""
	if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
		t.Fatal(e)
	}
	cfg, _ = siteconfig.Load(ctx, db)
	cfg.AuthMode = "email"
	if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
		t.Fatal(e)
	}
	if w := call(app.verifyEmail, `{"email":"`+email+`","code":"123456"}`); w.Code != 401 {
		t.Fatal("old-mode code accepted after switch")
	}
	req := httptest.NewRequest("GET", "/auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	w = httptest.NewRecorder()
	app.session(w, req)
	if w.Code != 200 {
		t.Fatal("mode change destroyed existing session")
	}
	db.Exec(`UPDATE users SET suspended=true WHERE id=$1`, auth.User.ID)
	w = httptest.NewRecorder()
	app.session(w, req)
	if w.Code != 401 {
		t.Fatal("suspended account retained access", w.Code)
	}
	cache.Del(ctx, sessionKey(auth.Token), oldKey, challengeKey(current, email)+":resend")
	cfg, _ = siteconfig.Load(ctx, db)
	cfg.AuthMode = "telegram"
	if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
		t.Fatal(e)
	}
}
