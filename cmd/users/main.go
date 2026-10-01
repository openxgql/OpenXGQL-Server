package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/siteconfig"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

type User struct {
	ID         string    `json:"id"`
	ScreenName string    `json:"screen_name"`
	Name       string    `json:"name"`
	Bio        string    `json:"bio"`
	Location   string    `json:"location"`
	Website    string    `json:"website"`
	AvatarURL  string    `json:"avatar_url"`
	Verified   bool      `json:"verified"`
	CreatedAt  time.Time `json:"created_at"`
}

type App struct {
	db    *sql.DB
	cache *redis.Client
}

var screenName = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

func main() {
	app := &App{db: common.OpenDB(), cache: common.OpenRedis()}
	_, err := app.db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		screen_name VARCHAR(15) NOT NULL,
		name VARCHAR(50) NOT NULL,
		bio VARCHAR(160) NOT NULL DEFAULT '',
		location VARCHAR(30) NOT NULL DEFAULT '',
		website VARCHAR(100) NOT NULL DEFAULT '',
		avatar_url TEXT NOT NULL DEFAULT '',
		email TEXT,
		verified BOOLEAN NOT NULL DEFAULT false,
		password_hash TEXT,
		telegram_id BIGINT UNIQUE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_screen_name_lower_idx ON users (lower(screen_name))`)
	if err != nil {
		panic(err)
	}
	for _, query := range []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS bio VARCHAR(160) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS location VARCHAR(30) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS website VARCHAR(100) NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS verified BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS suspended BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS protected BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS premium BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS telegram_id BIGINT`,
	} {
		if _, err := app.db.Exec(query); err != nil {
			panic(err)
		}
	}
	if _, err := app.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_idx ON users (lower(email)) WHERE email IS NOT NULL`); err != nil {
		panic(err)
	}
	if _, err := app.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_telegram_id_idx ON users (telegram_id) WHERE telegram_id IS NOT NULL`); err != nil {
		panic(err)
	}
	if err := app.initFollows(); err != nil {
		panic(err)
	}
	if err := app.initPremium(); err != nil {
		panic(err)
	}
	if _, err := app.db.Exec(siteconfig.Schema); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.health)
	mux.HandleFunc("POST /auth/register", app.register)
	mux.HandleFunc("POST /auth/login", app.login)
	mux.HandleFunc("GET /auth/session", app.session)
	mux.HandleFunc("POST /auth/logout", app.logout)
	mux.HandleFunc("POST /auth/email/start", app.startEmail)
	mux.HandleFunc("POST /auth/email/verify", app.verifyEmail)
	mux.HandleFunc("GET /auth/config", app.authConfig)
	mux.Handle("POST /auth/telegram/register", app.telegramOnly(http.HandlerFunc(app.telegramRegister)))
	mux.Handle("GET /auth/telegram/account", app.telegramOnly(http.HandlerFunc(app.telegramAccount)))
	mux.Handle("POST /auth/telegram/premium", app.telegramOnly(http.HandlerFunc(app.telegramPremium)))
	mux.HandleFunc("GET /users/by-name/{screenName}", app.byName)
	mux.HandleFunc("GET /users/search", app.searchUsers)
	mux.HandleFunc("GET /follows/{id}/relationship", app.relationship)
	mux.HandleFunc("PUT /follows/{id}/followers/{followerID}", app.follow)
	mux.HandleFunc("DELETE /follows/{id}/followers/{followerID}", app.follow)
	mux.HandleFunc("GET /follows/{id}/{kind}", app.followList)
	mux.HandleFunc("GET /users/{id}", app.byID)
	mux.HandleFunc("PATCH /users/{id}", app.update)
	common.Serve(common.Env("LISTEN_ADDR", "127.0.0.1:8081"), mux)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if a.db.PingContext(ctx) != nil || a.cache.Ping(ctx).Err() != nil {
		common.Error(w, 503, "dependency unavailable")
		return
	}
	common.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *App) byID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		common.Error(w, 400, "invalid user id")
		return
	}
	a.lookup(w, r, "user:id:"+id, `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at FROM users WHERE id = $1`, id)
}

func (a *App) byName(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("screenName")
	if !screenName.MatchString(name) {
		common.Error(w, 400, "invalid screen name")
		return
	}
	a.lookup(w, r, "user:name:"+strings.ToLower(name), `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at FROM users WHERE lower(screen_name) = lower($1)`, name)
}

func (a *App) lookup(w http.ResponseWriter, r *http.Request, key, query, arg string) {
	if cached, err := a.cache.Get(r.Context(), key).Bytes(); err == nil {
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(append(cached, '\n'))
		return
	}
	var id int64
	var user User
	err := a.db.QueryRowContext(r.Context(), query, arg).Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "user not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	user.ID = strconv.FormatInt(id, 10)
	a.cacheUser(r.Context(), user)
	common.WriteJSON(w, 200, user)
}

func (a *App) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		common.Error(w, 400, "invalid user id")
		return
	}
	var input struct {
		ScreenName *string `json:"screen_name"`
		Name       *string `json:"name"`
		Bio        *string `json:"bio"`
		Location   *string `json:"location"`
		Website    *string `json:"website"`
		AvatarURL  *string `json:"avatar_url"`
		Verified   *bool   `json:"verified"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	if input.ScreenName == nil && input.Name == nil && input.Bio == nil && input.Location == nil && input.Website == nil && input.AvatarURL == nil && input.Verified == nil {
		common.Error(w, 400, "at least one profile field is required")
		return
	}
	if input.ScreenName != nil && !screenName.MatchString(*input.ScreenName) {
		common.Error(w, 400, "screen_name must be 1-15 letters, digits or underscores")
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if *input.Name == "" || len([]rune(*input.Name)) > 50 {
			common.Error(w, 400, "name must be 1-50 characters")
			return
		}
	}
	if input.Bio != nil && len([]rune(*input.Bio)) > 160 {
		common.Error(w, 400, "bio must be at most 160 characters")
		return
	}
	if input.Location != nil && len([]rune(*input.Location)) > 30 {
		common.Error(w, 400, "location must be at most 30 characters")
		return
	}
	if input.Website != nil {
		*input.Website = strings.TrimSpace(*input.Website)
		if *input.Website != "" && !strings.Contains(*input.Website, "://") {
			*input.Website = "https://" + *input.Website
		}
		if len(*input.Website) > 100 {
			common.Error(w, 400, "website must be at most 100 characters")
			return
		}
		if *input.Website != "" {
			parsed, err := url.Parse(*input.Website)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
				common.Error(w, 400, "website must be an HTTP(S) URL")
				return
			}
		}
	}
	if input.AvatarURL != nil && *input.AvatarURL != "" {
		parsed, err := url.Parse(*input.AvatarURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || len(*input.AvatarURL) > 2048 {
			common.Error(w, 400, "avatar_url must be an HTTPS URL")
			return
		}
	}
	var user User
	var numericID int64
	var oldScreenName string
	if err := a.db.QueryRowContext(r.Context(), `SELECT screen_name FROM users WHERE id=$1`, id).Scan(&oldScreenName); errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "user not found")
		return
	} else if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	err := a.db.QueryRowContext(r.Context(), `UPDATE users SET screen_name=COALESCE($1,screen_name), name=COALESCE($2,name), bio=COALESCE($3,bio), location=COALESCE($4,location), website=COALESCE($5,website), avatar_url=COALESCE($6,avatar_url), verified=COALESCE($7,verified)
		WHERE id=$8 RETURNING id, screen_name, name, bio, location, website, avatar_url, verified, created_at`, input.ScreenName, input.Name, input.Bio, input.Location, input.Website, input.AvatarURL, input.Verified, id).
		Scan(&numericID, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "user not found")
		return
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			common.Error(w, 409, "screen_name already exists")
			return
		}
		common.Error(w, 500, "database error")
		return
	}
	user.ID = strconv.FormatInt(numericID, 10)
	_ = a.cache.Del(r.Context(), "user:id:"+user.ID, "user:name:"+strings.ToLower(oldScreenName), "user:name:"+strings.ToLower(user.ScreenName)).Err()
	a.cacheUser(r.Context(), user)
	common.WriteJSON(w, 200, user)
}

func (a *App) cacheUser(ctx context.Context, user User) {
	value, err := json.Marshal(user)
	if err != nil {
		return
	}
	pipe := a.cache.Pipeline()
	pipe.Set(ctx, "user:id:"+user.ID, value, 5*time.Minute)
	pipe.Set(ctx, "user:name:"+strings.ToLower(user.ScreenName), value, 5*time.Minute)
	_, _ = pipe.Exec(ctx)
}
