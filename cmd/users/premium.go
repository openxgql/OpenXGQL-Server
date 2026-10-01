package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
)

const premiumStars = 3

func (a *App) initPremium() error {
	_, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS premium_payments (
		charge_id TEXT PRIMARY KEY,
		user_id BIGINT NOT NULL REFERENCES users(id),
		telegram_id BIGINT NOT NULL,
		currency TEXT NOT NULL CHECK (currency = 'XTR'),
		amount INTEGER NOT NULL CHECK (amount = 3),
		invoice_payload TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);
	CREATE TABLE IF NOT EXISTS fl_pending_premium_payments (
		charge_id TEXT PRIMARY KEY,
		user_id BIGINT NOT NULL REFERENCES users(id),
		telegram_id BIGINT NOT NULL,
		currency TEXT NOT NULL CHECK(currency='XTR'),
		amount INTEGER NOT NULL CHECK(amount=3),
		invoice_payload TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

func (a *App) telegramPremium(w http.ResponseWriter, r *http.Request) {
	if !telegramAuthorized(r) {
		common.Error(w, 403, "forbidden")
		return
	}
	var input struct {
		ChatID         int64  `json:"chat_id"`
		UserID         string `json:"user_id"`
		Currency       string `json:"currency"`
		Amount         int64  `json:"amount"`
		ChargeID       string `json:"charge_id"`
		InvoicePayload string `json:"invoice_payload"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	userID, err := strconv.ParseInt(input.UserID, 10, 64)
	if err != nil || userID <= 0 || input.ChatID <= 0 || input.Currency != "XTR" || input.Amount != premiumStars ||
		input.ChargeID == "" || len(input.ChargeID) > 256 || input.InvoicePayload == "" || len(input.InvoicePayload) > 128 {
		common.Error(w, 400, "invalid premium payment")
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var currentID int64
	err = tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=$1 AND telegram_id=$2 AND NOT suspended FOR UPDATE`, userID, input.ChatID).Scan(&currentID)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "Telegram account not registered")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	var paidFor int64
	err = tx.QueryRowContext(r.Context(), `SELECT user_id FROM premium_payments WHERE charge_id=$1`, input.ChargeID).Scan(&paidFor)
	if err == nil {
		if paidFor != userID {
			common.Error(w, 409, "payment already assigned")
			return
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO premium_payments (charge_id,user_id,telegram_id,currency,amount,invoice_payload)
			VALUES ($1,$2,$3,$4,$5,$6)`, input.ChargeID, userID, input.ChatID, input.Currency, input.Amount, input.InvoicePayload)
		if err != nil {
			common.Error(w, 500, "database error")
			return
		}
	} else {
		common.Error(w, 500, "database error")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET premium=true,verified=true WHERE id=$1`, userID); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	if err = tx.Commit(); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	_ = a.cache.Del(r.Context(), "user:id:"+input.UserID).Err()
	var screen string
	if a.db.QueryRowContext(r.Context(), `SELECT screen_name FROM users WHERE id=$1`, userID).Scan(&screen) == nil {
		_ = a.cache.Del(r.Context(), "user:name:"+strings.ToLower(screen)).Err()
	}
	common.WriteJSON(w, 200, map[string]any{"premium": true, "verified": true})
}
