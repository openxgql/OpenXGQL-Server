package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"flashlarry-api/internal/telegramapi"
)

func TestPremiumPayloadIsBoundToAccountAndChat(t *testing.T) {
	payload, err := premiumPayload("secret", "42", 123)
	if err != nil {
		t.Fatal(err)
	}
	if userID, ok := verifyPremiumPayload("secret", payload, 123); !ok || userID != "42" {
		t.Fatalf("valid payload rejected: %q, %v", userID, ok)
	}
	if _, ok := verifyPremiumPayload("secret", payload, 124); ok {
		t.Fatal("payload accepted for another Telegram account")
	}
	if _, ok := verifyPremiumPayload("wrong", payload, 123); ok {
		t.Fatal("payload accepted with another secret")
	}
	if _, ok := verifyPremiumPayload("secret", strings.Replace(payload, ":42:", ":43:", 1), 123); ok {
		t.Fatal("modified user id accepted")
	}
}

func TestPremiumGrantedOnlyAfterSuccessfulPayment(t *testing.T) {
	var payload string
	var approved bool
	grants := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/telegram/account":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "42", "screen_name": "alice", "premium": false})
		case "/auth/telegram/premium":
			grants++
			_ = json.NewEncoder(w).Encode(map[string]any{"premium": true})
		case "/botsecret/sendInvoice":
			var invoice struct {
				Payload  string `json:"payload"`
				Currency string `json:"currency"`
				Prices   []struct {
					Amount int `json:"amount"`
				} `json:"prices"`
			}
			_ = json.NewDecoder(r.Body).Decode(&invoice)
			if invoice.Currency != "XTR" || len(invoice.Prices) != 1 || invoice.Prices[0].Amount != 3 {
				t.Errorf("wrong invoice: %+v", invoice)
			}
			payload = invoice.Payload
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case "/botsecret/answerPreCheckoutQuery":
			var answer struct {
				OK bool `json:"ok"`
			}
			_ = json.NewDecoder(r.Body).Decode(&answer)
			approved = answer.OK
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case "/botsecret/sendMessage":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	bot := &Bot{tg: telegramapi.New("secret", server.URL), client: server.Client(), usersURL: server.URL, token: "secret"}
	ctx := context.Background()
	if err := bot.sendPremiumInvoice(ctx, 123); err != nil {
		t.Fatal(err)
	}
	if payload == "" || grants != 0 {
		t.Fatal("invoice did not arrive or granted before payment")
	}
	if err := bot.preCheckout(ctx, &telegramapi.PreCheckoutQuery{ID: "checkout", From: telegramapi.User{ID: 123}, Currency: "XTR", TotalAmount: 3, InvoicePayload: payload}); err != nil {
		t.Fatal(err)
	}
	if !approved || grants != 0 {
		t.Fatal("checkout was rejected or granted before successful payment")
	}
	message := &telegramapi.Message{Chat: telegramapi.Chat{ID: 123}, From: telegramapi.User{ID: 123}}
	payment := &telegramapi.SuccessfulPayment{Currency: "XTR", TotalAmount: 3, InvoicePayload: payload, TelegramPaymentChargeID: "charge"}
	if err := bot.successfulPayment(ctx, message, payment); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Fatalf("got %d grants, want 1", grants)
	}
}
