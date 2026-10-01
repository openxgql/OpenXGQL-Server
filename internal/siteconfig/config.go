package siteconfig

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"strings"
)

type SMTP struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Username          string `json:"username"`
	From              string `json:"from"`
	TLS               string `json:"tls"`
	Password          string `json:"password,omitempty"`
	PasswordEncrypted string `json:"password_encrypted,omitempty"`
	PasswordSet       bool   `json:"password_set"`
}
type Config struct {
	FeedMode     string `json:"feed_mode"`
	AuthMode     string `json:"auth_mode"`
	Revision     int64  `json:"revision"`
	AuthRevision int64  `json:"auth_revision"`
	SMTP         SMTP   `json:"smtp"`
}

const Schema = `CREATE TABLE IF NOT EXISTS fl_site_config (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton), feed_mode TEXT NOT NULL DEFAULT 'latest' CHECK(feed_mode IN ('latest','algorithm')),
 auth_mode TEXT NOT NULL DEFAULT 'telegram' CHECK(auth_mode IN ('telegram','email')), revision BIGINT NOT NULL DEFAULT 1, auth_revision BIGINT NOT NULL DEFAULT 1,
 smtp JSONB NOT NULL DEFAULT '{}', updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
 INSERT INTO fl_site_config(singleton) VALUES(true) ON CONFLICT DO NOTHING;
 CREATE TABLE IF NOT EXISTS fl_account_priorities(user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, priority INTEGER NOT NULL CHECK(priority BETWEEN -100 AND 100), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());`

type Scanner interface{ Scan(...any) error }

func scan(row Scanner) (Config, error) {
	var c Config
	var raw []byte
	err := row.Scan(&c.FeedMode, &c.AuthMode, &c.Revision, &c.AuthRevision, &raw)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c.SMTP)
	c.SMTP.PasswordSet = c.SMTP.PasswordEncrypted != ""
	return c, err
}
func Load(ctx context.Context, db *sql.DB) (Config, error) {
	return scan(db.QueryRowContext(ctx, `SELECT feed_mode,auth_mode,revision,auth_revision,smtp FROM fl_site_config WHERE singleton`))
}
func Locked(ctx context.Context, tx *sql.Tx) (Config, error) {
	return scan(tx.QueryRowContext(ctx, `SELECT feed_mode,auth_mode,revision,auth_revision,smtp FROM fl_site_config WHERE singleton FOR SHARE`))
}
func (c Config) Public() Config { c.SMTP.Password = ""; c.SMTP.PasswordEncrypted = ""; return c }
func (s SMTP) Validate() error {
	if s.Host == "" || strings.ContainsAny(s.Host, "\r\n /\\") || s.Port < 1 || s.Port > 65535 {
		return errors.New("укажите SMTP-сервер и порт")
	}
	if s.TLS != "starttls" && s.TLS != "tls" {
		return errors.New("SMTP требует STARTTLS или TLS")
	}
	a, e := mail.ParseAddress(s.From)
	if e != nil || a.Address != s.From || strings.ContainsAny(s.From+s.Username, "\r\n") {
		return errors.New("некорректный адрес отправителя")
	}
	if s.Username != "" && s.PasswordEncrypted == "" && s.Password == "" {
		return errors.New("укажите пароль SMTP")
	}
	return nil
}
func Key() ([]byte, error) {
	b, e := base64.StdEncoding.DecodeString(os.Getenv("SETTINGS_ENCRYPTION_KEY"))
	if e != nil || len(b) != 32 {
		return nil, errors.New("settings encryption key unavailable")
	}
	return b, nil
}
func encrypt(s string) (string, error) {
	key, e := Key()
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(s), []byte("flashlarry-smtp-v1"))), nil
}
func (s SMTP) Secret() (string, error) {
	if s.PasswordEncrypted == "" {
		return "", nil
	}
	key, e := Key()
	if e != nil {
		return "", e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	b, e := base64.StdEncoding.DecodeString(s.PasswordEncrypted)
	if e != nil || len(b) < g.NonceSize() {
		return "", errors.New("invalid encrypted SMTP password")
	}
	plain, e := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte("flashlarry-smtp-v1"))
	return string(plain), e
}
func Save(ctx context.Context, db *sql.DB, next Config, actor string) (Config, error) {
	if next.FeedMode != "latest" && next.FeedMode != "algorithm" {
		return Config{}, errors.New("некорректный режим ленты")
	}
	if next.AuthMode != "telegram" && next.AuthMode != "email" {
		return Config{}, errors.New("некорректный режим входа")
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return Config{}, e
	}
	defer tx.Rollback()
	current, e := scan(tx.QueryRowContext(ctx, `SELECT feed_mode,auth_mode,revision,auth_revision,smtp FROM fl_site_config WHERE singleton FOR UPDATE`))
	if e != nil {
		return Config{}, e
	}
	if next.Revision != current.Revision {
		return Config{}, errors.New("настройки уже изменены; обновите страницу")
	}
	next.SMTP.PasswordEncrypted = current.SMTP.PasswordEncrypted
	if next.SMTP.Password != "" {
		next.SMTP.PasswordEncrypted, e = encrypt(next.SMTP.Password)
		if e != nil {
			return Config{}, e
		}
	}
	next.SMTP.Password = ""
	next.SMTP.PasswordSet = next.SMTP.PasswordEncrypted != ""
	if next.SMTP.Host != "" || next.AuthMode == "email" {
		if e = next.SMTP.Validate(); e != nil {
			return Config{}, e
		}
		if _, e = next.SMTP.Secret(); e != nil {
			return Config{}, e
		}
	}
	oldSMTP, _ := json.Marshal(current.SMTP)
	newSMTP, _ := json.Marshal(next.SMTP)
	next.Revision = current.Revision + 1
	next.AuthRevision = current.AuthRevision
	if current.AuthMode != next.AuthMode || string(oldSMTP) != string(newSMTP) {
		next.AuthRevision++
	}
	if _, e = tx.ExecContext(ctx, `UPDATE fl_site_config SET feed_mode=$1,auth_mode=$2,revision=$3,auth_revision=$4,smtp=$5,updated_at=now() WHERE singleton`, next.FeedMode, next.AuthMode, next.Revision, next.AuthRevision, newSMTP); e != nil {
		return Config{}, e
	}
	audit, _ := json.Marshal(map[string]any{"feed_mode": next.FeedMode, "auth_mode": next.AuthMode, "smtp_changed": string(oldSMTP) != string(newSMTP)})
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'site_config','global',$2)`, actor, audit); e != nil {
		return Config{}, e
	}
	if e = tx.Commit(); e != nil {
		return Config{}, e
	}
	return next.Public(), nil
}
func (s SMTP) Address() string { return net.JoinHostPort(s.Host, fmt.Sprint(s.Port)) }
