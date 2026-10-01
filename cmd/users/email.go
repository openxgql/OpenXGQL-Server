package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/mailcode"
	"flashlarry-api/internal/siteconfig"
	"flashlarry-api/internal/telegramapi"
	"github.com/redis/go-redis/v9"
)

func normalizedTestEmail(value string) string {
	value = normalizedEmail(value)
	if !strings.HasSuffix(value, "@flashlarry.test") {
		return ""
	}
	return value
}
func normalizedEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, e := mail.ParseAddress(value)
	if e != nil || parsed.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}
func emailForMode(value, mode string) string {
	if mode == "telegram" {
		return normalizedEmail(value)
	}
	value = normalizedEmail(value)
	parts := strings.Split(value, "@")
	if len(parts) != 2 || !strings.Contains(parts[1], ".") {
		return ""
	}
	for _, suffix := range []string{".test", ".invalid", ".localhost", ".example"} {
		if strings.HasSuffix(parts[1], suffix) {
			return ""
		}
	}
	return value
}
func challengeKey(c siteconfig.Config, email string) string {
	x := sha256.Sum256([]byte(email))
	return fmt.Sprintf("email-challenge:%d:%s:%x", c.AuthRevision, c.AuthMode, x)
}
func emailCodeHash(email, code string) (string, error) {
	key, e := siteconfig.Key()
	if e != nil {
		return "", e
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(email + ":" + code))
	return hex.EncodeToString(m.Sum(nil)), nil
}
func (a *App) emailLimit(w http.ResponseWriter, r *http.Request, email string) bool {
	x := sha256.Sum256([]byte(email))
	key := fmt.Sprintf("email-attempts:%x", x)
	n, e := a.cache.Incr(r.Context(), key).Result()
	if e != nil {
		common.Error(w, 503, "authentication unavailable")
		return false
	}
	if n == 1 {
		a.cache.Expire(r.Context(), key, 10*time.Minute)
	}
	if n > 15 {
		common.Error(w, 429, "too many email attempts")
		return false
	}
	return true
}
func (a *App) startEmail(w http.ResponseWriter, r *http.Request) {
	if !a.rateLimit(w, r, "email-start", 10) {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	cfg, e := siteconfig.Load(r.Context(), a.db)
	if e != nil {
		common.Error(w, 503, "authentication settings unavailable")
		return
	}
	email := emailForMode(input.Email, cfg.AuthMode)
	if email == "" {
		common.Error(w, 400, map[string]string{"telegram": "email must end in @flashlarry.test", "email": "enter a real email address"}[cfg.AuthMode])
		return
	}
	if !a.emailLimit(w, r, email) {
		return
	}
	var chatID int64
	if cfg.AuthMode == "telegram" {
		e = a.db.QueryRowContext(r.Context(), `SELECT telegram_id FROM users WHERE lower(email)=$1 AND telegram_id IS NOT NULL AND NOT suspended`, email).Scan(&chatID)
		if errors.Is(e, sql.ErrNoRows) {
			common.Error(w, 409, "register in Telegram first")
			return
		}
		if e != nil {
			common.Error(w, 503, "account lookup unavailable")
			return
		}
	} else {
		if e = cfg.SMTP.Validate(); e != nil {
			common.Error(w, 503, "email delivery is not configured")
			return
		}
	}
	key := challengeKey(cfg, email)
	reserved, e := a.cache.SetNX(r.Context(), key+":resend", "1", time.Minute).Result()
	if e != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	if !reserved {
		common.Error(w, 429, "wait one minute before requesting another code")
		return
	}
	n, e := rand.Int(rand.Reader, big.NewInt(1000000))
	if e != nil {
		a.cache.Del(r.Context(), key+":resend")
		common.Error(w, 503, "verification code unavailable")
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	hash, e := emailCodeHash(email, code)
	if e != nil {
		a.cache.Del(r.Context(), key+":resend")
		common.Error(w, 503, "verification code unavailable")
		return
	}
	if e = a.cache.Set(r.Context(), key, hash, 5*time.Minute).Err(); e != nil {
		a.cache.Del(r.Context(), key+":resend")
		common.Error(w, 503, "authentication unavailable")
		return
	}
	if cfg.AuthMode == "email" {
		e = mailcode.Send(r.Context(), cfg.SMTP, email, code)
	} else {
		e = telegramapi.New(common.Env("TELEGRAM_BOT_TOKEN", ""), "").SendMessage(r.Context(), chatID, "Код входа FlashLarry: "+code+"\nДействует 5 минут. Не сообщайте его никому.", nil)
	}
	if e != nil {
		a.cache.Del(r.Context(), key, key+":resend")
		common.Error(w, 503, "could not deliver verification code")
		return
	}
	common.WriteJSON(w, 200, map[string]any{"email": email, "expires_in": 300, "delivery": cfg.AuthMode})
}

var consumeCode = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then redis.call('DEL',KEYS[1]); return 1 else return 0 end`)

func (a *App) verifyEmail(w http.ResponseWriter, r *http.Request) {
	if !a.rateLimit(w, r, "email-verify", 15) {
		return
	}
	var input struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	defer tx.Rollback()
	cfg, e := siteconfig.Locked(r.Context(), tx)
	if e != nil {
		common.Error(w, 503, "authentication settings unavailable")
		return
	}
	email := emailForMode(input.Email, cfg.AuthMode)
	if email == "" || len(input.Code) != 6 {
		common.Error(w, 401, "invalid verification code")
		return
	}
	if !a.emailLimit(w, r, email) {
		return
	}
	for _, d := range input.Code {
		if d < '0' || d > '9' {
			common.Error(w, 401, "invalid verification code")
			return
		}
	}
	hash, e := emailCodeHash(email, input.Code)
	if e != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	used, e := consumeCode.Run(r.Context(), a.cache, []string{challengeKey(cfg, email)}, hash).Int()
	if e != nil || used != 1 {
		common.Error(w, 401, "invalid, expired or already used verification code")
		return
	}
	var user User
	var id int64
	query := `SELECT id,screen_name,name,bio,location,website,avatar_url,verified,created_at FROM users WHERE lower(email)=$1 AND NOT suspended`
	if cfg.AuthMode == "telegram" {
		query += ` AND telegram_id IS NOT NULL`
	}
	e = tx.QueryRowContext(r.Context(), query, email).Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) && cfg.AuthMode == "email" {
		var exists bool
		if e = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, email).Scan(&exists); e != nil || exists {
			common.Error(w, 403, "account unavailable")
			return
		}
		base := regexpUsername(strings.Split(email, "@")[0])
		if len(base) > 12 {
			base = base[:12]
		}
		if base == "" {
			base = "user"
		}
		for attempt := 0; attempt < 1000; attempt++ {
			candidate := base
			if attempt > 0 {
				candidate += strconv.Itoa(attempt)
			}
			e = tx.QueryRowContext(r.Context(), `INSERT INTO users(email,screen_name,name) VALUES($1,$2,$3) ON CONFLICT DO NOTHING RETURNING id,screen_name,name,bio,location,website,avatar_url,verified,created_at`, email, candidate, base).Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt)
			if !errors.Is(e, sql.ErrNoRows) {
				break
			}
		}
	}
	if e != nil {
		common.Error(w, 401, "account unavailable")
		return
	}
	if e = tx.Commit(); e != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	user.ID = strconv.FormatInt(id, 10)
	token, e := a.newSession(r, user.ID)
	if e != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	a.cacheUser(r.Context(), user)
	common.WriteJSON(w, 200, map[string]any{"token": token, "user": user})
}
func regexpUsername(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
