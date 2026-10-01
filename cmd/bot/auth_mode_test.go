package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"flashlarry-api/internal/telegramapi"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestIntegrationBotDisabledInEmailMode(t *testing.T) {
	dsn := os.Getenv("FLASHLARRY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	if !strings.Contains(dsn, "flashlarry_integration") {
		t.Fatal("refusing production DB")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(`UPDATE fl_site_config SET auth_mode='email' WHERE singleton`); e != nil {
		t.Fatal(e)
	}
	defer db.Exec(`UPDATE fl_site_config SET auth_mode='telegram' WHERE singleton`)
	declines := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botsecret/answerPreCheckoutQuery" {
			t.Errorf("disabled bot called %s", r.URL.Path)
		}
		var answer struct {
			OK bool `json:"ok"`
		}
		if json.NewDecoder(r.Body).Decode(&answer) != nil || answer.OK {
			t.Error("disabled checkout accepted")
		}
		declines++
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer mock.Close()
	bot := &Bot{db: db, tg: telegramapi.New("secret", mock.URL), usersURL: mock.URL, client: mock.Client(), token: "secret"}
	message := telegramapi.Message{Chat: telegramapi.Chat{ID: 123, Type: "private"}, From: telegramapi.User{ID: 123}, Text: "/start"}
	for _, update := range []telegramapi.Update{
		{Message: &message},
		{Callback: &telegramapi.CallbackQuery{ID: "register", From: message.From, Message: message, Data: "register"}},
		{Callback: &telegramapi.CallbackQuery{ID: "premium", From: message.From, Message: message, Data: "premium"}},
		{PreCheckout: &telegramapi.PreCheckoutQuery{ID: "checkout", From: message.From, Currency: "XTR", TotalAmount: 3}},
	} {
		if e := bot.handle(context.Background(), update); e != nil {
			t.Fatal(e)
		}
	}
	if declines != 1 {
		t.Fatal("checkout not declined", declines)
	}
	var user string
	if e := db.QueryRow(`SELECT id::text FROM users WHERE screen_name='feedviewer'`).Scan(&user); e != nil {
		t.Fatal(e)
	}
	payload, e := premiumPayload("secret", user, message.From.ID)
	if e != nil {
		t.Fatal(e)
	}
	payment := &telegramapi.SuccessfulPayment{Currency: "XTR", TotalAmount: 3, InvoicePayload: payload, TelegramPaymentChargeID: "integration-deferred"}
	message.SuccessfulPayment = payment
	for i := 0; i < 2; i++ {
		if e := bot.handle(context.Background(), telegramapi.Update{Message: &message}); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM fl_reports WHERE reason='premium_payment' AND target_id=$1`, user).Scan(&count); e != nil || count != 1 {
		t.Fatal("deferred receipt not recorded exactly once", count, e)
	}
	var premium bool
	if e := db.QueryRow(`SELECT premium FROM users WHERE id=$1`, user).Scan(&premium); e != nil || premium {
		t.Fatal("email mode automatically granted premium", premium, e)
	}
}
