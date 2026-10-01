package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/siteconfig"
	"flashlarry-api/internal/social"
)

//go:embed index.html
var page []byte

type Admin struct {
	store    *social.Store
	hash     []byte
	username string
	client   *http.Client
}

func main() {
	hash, e := hex.DecodeString(common.Env("ADMIN_PASSWORD_SHA256", ""))
	if e != nil || len(hash) != 32 {
		log.Fatal("ADMIN_PASSWORD_SHA256 is required")
	}
	a := &Admin{store: &social.Store{DB: common.OpenDB(), Cache: common.OpenRedis()}, hash: hash, username: common.Env("ADMIN_USERNAME", "admin"), client: &http.Client{Timeout: 15 * time.Second}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		common.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.Handle("/api/", a.auth(http.HandlerFunc(a.api)))
	common.Serve(common.Env("LISTEN_ADDR", "127.0.0.1:8084"), mux)
}
func (a *Admin) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	_, _ = w.Write(page)
}
func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(v string) string      { x := sha256.Sum256([]byte(v)); return hex.EncodeToString(x[:]) }
func origin(r *http.Request) bool { return r.Header.Get("Origin") == "https://"+r.Host }
func (a *Admin) login(w http.ResponseWriter, r *http.Request) {
	if !origin(r) {
		common.Error(w, 403, "same-origin request required")
		return
	}
	ip := r.RemoteAddr
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		parts := strings.Split(x, ",")
		ip = strings.TrimSpace(parts[len(parts)-1])
	}
	key := "admin:login:" + digest(ip)
	n, e := a.store.Cache.Incr(r.Context(), key).Result()
	if e != nil {
		common.Error(w, 503, "session store unavailable")
		return
	}
	if n == 1 {
		a.store.Cache.Expire(r.Context(), key, 15*time.Minute)
	}
	if n > 10 {
		common.Error(w, 429, "too many login attempts")
		return
	}
	var v struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&v) != nil {
		common.Error(w, 400, "invalid login")
		return
	}
	hash := sha256.Sum256([]byte(v.Password))
	if subtle.ConstantTimeCompare(hash[:], a.hash) != 1 || subtle.ConstantTimeCompare([]byte(v.Username), []byte(a.username)) != 1 {
		common.Error(w, 401, "invalid credentials")
		return
	}
	session, csrf := token(), token()
	if e = a.store.Cache.Set(r.Context(), "admin:session:"+digest(session), a.username+":"+csrf, 8*time.Hour).Err(); e != nil {
		common.Error(w, 503, "session store unavailable")
		return
	}
	a.store.Cache.Del(r.Context(), key)
	http.SetCookie(w, &http.Cookie{Name: "flashlarry_admin", Value: session, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	common.WriteJSON(w, 200, map[string]string{"csrf": csrf, "username": a.username})
}
func (a *Admin) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("flashlarry_admin")
		if e != nil || len(c.Value) != 64 {
			common.Error(w, 401, "admin login required")
			return
		}
		session, e := a.store.Cache.Get(r.Context(), "admin:session:"+digest(c.Value)).Result()
		parts := strings.SplitN(session, ":", 2)
		if e != nil || len(parts) != 2 {
			common.Error(w, 401, "admin session expired")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !origin(r) || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
				common.Error(w, 403, "invalid CSRF token")
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		r.Header.Set("X-Admin-Actor", parts[0])
		r.Header.Set("X-Admin-CSRF", parts[1])
		next.ServeHTTP(w, r)
	})
}
func (a *Admin) api(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/")
	actor := r.Header.Get("X-Admin-Actor")
	ctx := r.Context()
	var result any
	var e error
	if p == "session" && r.Method == "GET" {
		common.WriteJSON(w, 200, map[string]string{"username": actor, "csrf": r.Header.Get("X-Admin-CSRF")})
		return
	}
	if p == "logout" && r.Method == "POST" {
		c, _ := r.Cookie("flashlarry_admin")
		a.store.Cache.Del(ctx, "admin:session:"+digest(c.Value))
		http.SetCookie(w, &http.Cookie{Name: "flashlarry_admin", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		common.WriteJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if strings.HasPrefix(p, "media") {
		a.media(w, r)
		return
	}
	switch {
	case p == "settings" && r.Method == "GET":
		var cfg siteconfig.Config
		cfg, e = siteconfig.Load(ctx, a.store.DB)
		result = cfg.Public()
	case p == "users" && r.Method == "GET":
		result, e = a.store.AdminUsers(ctx, r.URL.Query().Get("q"))
	case p == "reports" && r.Method == "GET":
		result, e = a.store.AdminReports(ctx, r.URL.Query().Get("status"))
	case p == "ads" && r.Method == "GET":
		result, e = a.store.AdminAds(ctx)
	default:
		var v map[string]any
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if json.NewDecoder(r.Body).Decode(&v) != nil {
			common.Error(w, 400, "JSON object required")
			return
		}
		switch {
		case p == "settings" && r.Method == "POST":
			var cfg siteconfig.Config
			raw, _ := json.Marshal(v)
			if json.Unmarshal(raw, &cfg) != nil {
				common.Error(w, 400, "invalid settings")
				return
			}
			result, e = siteconfig.Save(ctx, a.store.DB, cfg, actor)
			if e != nil {
				common.Error(w, 400, e.Error())
				return
			}
		case p == "premium" && r.Method == "POST":
			e = a.store.SetPremium(ctx, social.Str(v, "user_id"), social.Bool(v, "premium"), actor)
		case p == "suspend" && r.Method == "POST":
			e = a.store.SetSuspended(ctx, social.Str(v, "user_id"), social.Bool(v, "suspended"), actor)
		case p == "priority" && r.Method == "POST":
			priority, ok := v["priority"].(float64)
			if !ok || math.Trunc(priority) != priority || priority < -100 || priority > 100 {
				common.Error(w, 400, "priority must be an integer between -100 and 100")
				return
			}
			e = a.store.SetPriority(ctx, social.Str(v, "user_id"), int(priority), actor)
		case p == "email" && r.Method == "POST":
			e = a.store.SetEmail(ctx, social.Str(v, "user_id"), social.Str(v, "email"), actor)
		case p == "verify" && r.Method == "POST":
			e = a.store.SetVerified(ctx, social.Str(v, "user_id"), social.Bool(v, "verified"), actor)
		case p == "review" && r.Method == "POST":
			e = a.store.ReviewReport(ctx, social.Str(v, "report_id"), social.Str(v, "action"), social.Str(v, "note"), actor)
		case p == "ads" && r.Method == "POST":
			result, e = a.store.CreateAd(ctx, v, actor)
		case p == "ads/active" && r.Method == "POST":
			e = a.store.SetAdActive(ctx, social.Str(v, "id"), social.Bool(v, "active"), actor)
		case p == "catalog" && r.Method == "POST":
			result, e = a.store.Catalog(ctx, social.Str(v, "kind"), social.Encode(v["data"]), actor)
		default:
			common.Error(w, 404, "admin route not found")
			return
		}
	}
	if e != nil {
		common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
		return
	}
	if result == nil {
		result = map[string]bool{"ok": true}
	}
	common.WriteJSON(w, 200, result)
}
func (a *Admin) media(w http.ResponseWriter, r *http.Request) {
	var id string
	e := a.store.DB.QueryRowContext(r.Context(), `SELECT id::text FROM users WHERE lower(screen_name)=lower($1) AND NOT suspended`, r.URL.Query().Get("screen_name")).Scan(&id)
	if e != nil {
		common.Error(w, 404, "advertiser not found")
		return
	}
	q := r.URL.Query()
	q.Del("screen_name")
	req, e := http.NewRequestWithContext(r.Context(), r.Method, common.Env("TWEETS_URL", "http://127.0.0.1:8082")+"/media/upload?"+q.Encode(), http.MaxBytesReader(w, r.Body, 8<<20))
	if e != nil {
		common.Error(w, 400, "invalid upload")
		return
	}
	req.Header.Set("X-Flashlarry-User-ID", id)
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	resp, e := a.client.Do(req)
	if e != nil {
		common.Error(w, 502, "media service unavailable")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}
