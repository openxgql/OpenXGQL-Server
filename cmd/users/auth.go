package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
)

const sessionTTL = 7 * 24 * time.Hour

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-account-password"), bcrypt.DefaultCost)

func (a *App) rateLimit(w http.ResponseWriter, r *http.Request, operation string, maximum int64) bool {
	ip := r.Header.Get("X-Client-IP")
	if ip == "" {
		ip, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	digest := sha256.Sum256([]byte(ip))
	key := "auth-rate:" + operation + ":" + hex.EncodeToString(digest[:]) + ":" + time.Now().UTC().Format("200601021504")
	count, err := a.cache.Incr(r.Context(), key).Result()
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return false
	}
	if count == 1 {
		_ = a.cache.Expire(r.Context(), key, 2*time.Minute).Err()
	}
	if count > maximum {
		common.Error(w, 429, "too many authentication attempts")
		return false
	}
	return true
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	if !a.rateLimit(w, r, "register", 10) {
		return
	}
	var input struct {
		ScreenName string `json:"screen_name"`
		Name       string `json:"name"`
		Password   string `json:"password"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	input.ScreenName = strings.TrimSpace(input.ScreenName)
	input.Name = strings.TrimSpace(input.Name)
	if !screenName.MatchString(input.ScreenName) || input.Name == "" || len([]rune(input.Name)) > 50 {
		common.Error(w, 400, "screen_name must be 1-15 letters, digits or underscores; name must be 1-50 characters")
		return
	}
	if len(input.Password) < 12 || len(input.Password) > 72 {
		common.Error(w, 400, "password must be 12-72 bytes")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		common.Error(w, 500, "password hashing failed")
		return
	}
	var user User
	var id int64
	err = a.db.QueryRowContext(r.Context(), `INSERT INTO users (screen_name, name, password_hash) VALUES ($1,$2,$3)
		RETURNING id, screen_name, name, bio, location, website, avatar_url, verified, created_at`, input.ScreenName, input.Name, string(hash)).
		Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			common.Error(w, 409, "screen_name already exists")
			return
		}
		common.Error(w, 500, "database error")
		return
	}
	user.ID = strconv.FormatInt(id, 10)
	a.cacheUser(r.Context(), user)
	token, err := a.newSession(r, user.ID)
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	common.WriteJSON(w, 201, map[string]any{"token": token, "user": user})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.rateLimit(w, r, "login", 20) {
		return
	}
	var input struct {
		ScreenName string `json:"screen_name"`
		Password   string `json:"password"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	var user User
	var hash sql.NullString
	var id int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at, password_hash
		FROM users WHERE lower(screen_name)=lower($1) AND NOT suspended`, input.ScreenName).
		Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 500, "database error")
		return
	}
	comparisonHash := dummyHash
	if err == nil && hash.Valid {
		comparisonHash = []byte(hash.String)
	}
	if bcrypt.CompareHashAndPassword(comparisonHash, []byte(input.Password)) != nil || err != nil || !hash.Valid {
		common.Error(w, 401, "invalid credentials")
		return
	}
	user.ID = strconv.FormatInt(id, 10)
	token, err := a.newSession(r, user.ID)
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	common.WriteJSON(w, 200, map[string]any{"token": token, "user": user})
}

func sessionKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return "session:" + hex.EncodeToString(digest[:])
}

func (a *App) newSession(r *http.Request, userID string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	if err := a.cache.Set(r.Context(), sessionKey(token), userID, sessionTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(header, "Bearer ")
}

func (a *App) session(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if len(token) != 64 {
		common.Error(w, 401, "invalid session")
		return
	}
	id, err := a.cache.Get(r.Context(), sessionKey(token)).Result()
	if err != nil {
		common.Error(w, 401, "invalid session")
		return
	}
	var user User
	var numericID int64
	err = a.db.QueryRowContext(r.Context(), `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at FROM users WHERE id=$1 AND NOT suspended`, id).
		Scan(&numericID, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
	if err != nil {
		common.Error(w, 401, "invalid session")
		return
	}
	user.ID = strconv.FormatInt(numericID, 10)
	common.WriteJSON(w, 200, user)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if len(token) != 64 {
		common.Error(w, 401, "invalid session")
		return
	}
	deleted, err := a.cache.Del(r.Context(), sessionKey(token)).Result()
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	common.WriteJSON(w, 200, map[string]bool{"logged_out": deleted > 0})
}
