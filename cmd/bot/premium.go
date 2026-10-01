package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"flashlarry-api/internal/telegramapi"
)

const premiumStars = 3

func premiumPayload(token, userID string, chatID int64) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	message := "premium:v1:" + userID + ":" + strconv.FormatInt(chatID, 10) + ":" + hex.EncodeToString(nonce[:])
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(message))
	return message + ":" + hex.EncodeToString(mac.Sum(nil)[:16]), nil
}

func verifyPremiumPayload(token, payload string, chatID int64) (string, bool) {
	parts := strings.Split(payload, ":")
	if len(parts) != 6 || parts[0] != "premium" || parts[1] != "v1" || len(payload) > 128 {
		return "", false
	}
	userID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || userID <= 0 {
		return "", false
	}
	boundChatID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || boundChatID != chatID || boundChatID <= 0 {
		return "", false
	}
	if nonce, err := hex.DecodeString(parts[4]); err != nil || len(nonce) != 12 {
		return "", false
	}
	signature, err := hex.DecodeString(parts[5])
	if err != nil || len(signature) != 16 {
		return "", false
	}
	message := strings.Join(parts[:5], ":")
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(message))
	if subtle.ConstantTimeCompare(signature, mac.Sum(nil)[:16]) != 1 {
		return "", false
	}
	return strconv.FormatInt(userID, 10), true
}

func (b *Bot) premiumAccount(ctx context.Context, chatID int64) (map[string]any, int, error) {
	var account map[string]any
	status, err := b.users(ctx, http.MethodGet, "/auth/telegram/account?chat_id="+strconv.FormatInt(chatID, 10), nil, &account)
	return account, status, err
}

func (b *Bot) sendPremiumInvoice(ctx context.Context, chatID int64) error {
	account, status, err := b.premiumAccount(ctx, chatID)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return b.send(ctx, chatID, "Сначала зарегистрируйтесь в этом боте.", menu())
	}
	if status != http.StatusOK {
		return errors.New("premium account lookup failed")
	}
	if account["premium"] == true {
		return b.send(ctx, chatID, "⭐ FlashLarry Premium уже активен. Галочка видна в профиле.", menu())
	}
	payload, err := premiumPayload(b.token, fmt.Sprint(account["id"]), chatID)
	if err != nil {
		return err
	}
	return b.tg.Call(ctx, "sendInvoice", map[string]any{
		"chat_id": chatID, "title": "FlashLarry Premium",
		"description": "Постоянный Premium и галочка в профиле @" + fmt.Sprint(account["screen_name"]),
		"payload":     payload, "currency": "XTR",
		"prices": []map[string]any{{"label": "FlashLarry Premium", "amount": premiumStars}},
	}, nil)
}

func (b *Bot) preCheckout(ctx context.Context, query *telegramapi.PreCheckoutQuery) error {
	userID, valid := verifyPremiumPayload(b.token, query.InvoicePayload, query.From.ID)
	valid = valid && query.Currency == "XTR" && query.TotalAmount == premiumStars
	if valid {
		account, status, err := b.premiumAccount(ctx, query.From.ID)
		if err != nil {
			return err
		}
		valid = status == http.StatusOK && fmt.Sprint(account["id"]) == userID && account["premium"] != true
	}
	answer := map[string]any{"pre_checkout_query_id": query.ID, "ok": valid}
	if !valid {
		answer["error_message"] = "Счёт недействителен или Premium уже активен. Откройте меню бота."
	}
	return b.tg.Call(ctx, "answerPreCheckoutQuery", answer, nil)
}

func (b *Bot) successfulPayment(ctx context.Context, message *telegramapi.Message, payment *telegramapi.SuccessfulPayment) error {
	userID, valid := verifyPremiumPayload(b.token, payment.InvoicePayload, message.From.ID)
	if !valid || payment.Currency != "XTR" || payment.TotalAmount != premiumStars || payment.TelegramPaymentChargeID == "" {
		return errors.New("invalid successful payment")
	}
	var result map[string]any
	status, err := b.users(ctx, http.MethodPost, "/auth/telegram/premium", map[string]any{
		"chat_id": message.From.ID, "user_id": userID,
		"currency": payment.Currency, "amount": payment.TotalAmount,
		"charge_id": payment.TelegramPaymentChargeID, "invoice_payload": payment.InvoicePayload,
	}, &result)
	if err != nil {
		return err
	}
	if status != http.StatusOK || result["premium"] != true {
		return fmt.Errorf("premium grant failed with HTTP %d", status)
	}
	return b.send(ctx, message.Chat.ID, "⭐ FlashLarry Premium активирован. Галочка появилась в профиле; обновите страницу.", menu())
}
