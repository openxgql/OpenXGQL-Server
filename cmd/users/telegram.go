package main

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
	"github.com/jackc/pgx/v5/pgconn"
)

func telegramAuthorized(r *http.Request) bool {
	token := common.Env("TELEGRAM_BOT_TOKEN", "")
	provided := r.Header.Get("X-Telegram-Bot-Token")
	return token != "" && len(provided) == len(token) && subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func (a *App) telegramRegister(w http.ResponseWriter, r *http.Request) {
	if !telegramAuthorized(r) {
		common.Error(w, 403, "forbidden")
		return
	}
	var input struct {
		ChatID    int64  `json:"chat_id"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	email := normalizedTestEmail(input.Email)
	if input.ChatID <= 0 || email == "" {
		common.Error(w, 400, "invalid Telegram account or email")
		return
	}
	var existing int64
	err := a.db.QueryRowContext(r.Context(), `SELECT id FROM users WHERE telegram_id=$1`, input.ChatID).Scan(&existing)
	if err == nil {
		common.Error(w, 409, "Telegram account is already registered")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 500, "database error")
		return
	}
	local := strings.SplitN(email, "@", 2)[0]
	username := local
	if !screenName.MatchString(username) {
		username = ""
	}
	name := strings.TrimSpace(input.FirstName)
	if name == "" {
		name = local
	}
	if len([]rune(name)) > 50 {
		name = string([]rune(name)[:50])
	}
	var user User
	var id int64
	for attempt := 0; attempt < 3; attempt++ {
		candidate := username
		if candidate == "" || attempt > 0 {
			var random [6]byte
			if _, err := rand.Read(random[:]); err != nil {
				common.Error(w, 500, "account creation failed")
				return
			}
			candidate = "u" + hex.EncodeToString(random[:])
		}
		err = a.db.QueryRowContext(r.Context(), `INSERT INTO users (email, screen_name, name, telegram_id) VALUES ($1,$2,$3,$4)
			RETURNING id, screen_name, name, bio, location, website, avatar_url, verified, created_at`, email, candidate, name, input.ChatID).
			Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
		if err == nil {
			user.ID = strconv.FormatInt(id, 10)
			a.cacheUser(r.Context(), user)
			common.WriteJSON(w, 201, user)
			return
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			break
		}
		if pgErr.ConstraintName == "users_screen_name_lower_idx" {
			continue
		}
		common.Error(w, 409, "email or Telegram account is already registered")
		return
	}
	common.Error(w, 500, "account creation failed")
}

func (a *App) telegramAccount(w http.ResponseWriter, r *http.Request) {
	if !telegramAuthorized(r) {
		common.Error(w, 403, "forbidden")
		return
	}
	chatID, err := strconv.ParseInt(r.URL.Query().Get("chat_id"), 10, 64)
	if err != nil || chatID <= 0 {
		common.Error(w, 400, "invalid Telegram account")
		return
	}
	var user User
	var id int64
	var premium bool
	err = a.db.QueryRowContext(r.Context(), `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at, premium
		FROM users WHERE telegram_id=$1 AND NOT suspended`, chatID).
		Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt, &premium)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "Telegram account not registered")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	user.ID = strconv.FormatInt(id, 10)
	common.WriteJSON(w, 200, struct {
		User
		Premium bool `json:"premium"`
	}{User: user, Premium: premium})
}
