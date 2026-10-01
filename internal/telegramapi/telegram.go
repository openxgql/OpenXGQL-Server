package telegramapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	token string
	base  string
	http  *http.Client
}

func New(token, base string) *Client {
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &Client{token: token, base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 45 * time.Second}}
}

func (c *Client) Call(ctx context.Context, method string, input, result any) error {
	if c.token == "" {
		return errors.New("Telegram bot token is not configured")
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Telegram %s request failed", method)
	}
	defer response.Body.Close()
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("invalid Telegram %s response", method)
	}
	if response.StatusCode != http.StatusOK || !envelope.OK {
		return fmt.Errorf("Telegram %s returned HTTP %d", method, response.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, message string, keyboard [][]Button) error {
	input := map[string]any{"chat_id": chatID, "text": message}
	if len(keyboard) != 0 {
		input["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	return c.Call(ctx, "sendMessage", input, nil)
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	Chat              Chat               `json:"chat"`
	From              User               `json:"from"`
	Text              string             `json:"text"`
	SuccessfulPayment *SuccessfulPayment `json:"successful_payment"`
}

type SuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int64  `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

type PreCheckoutQuery struct {
	ID             string `json:"id"`
	From           User   `json:"from"`
	Currency       string `json:"currency"`
	TotalAmount    int64  `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

type CallbackQuery struct {
	ID      string  `json:"id"`
	From    User    `json:"from"`
	Message Message `json:"message"`
	Data    string  `json:"data"`
}

type Update struct {
	ID          int64             `json:"update_id"`
	Message     *Message          `json:"message"`
	Callback    *CallbackQuery    `json:"callback_query"`
	PreCheckout *PreCheckoutQuery `json:"pre_checkout_query"`
}

func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var updates []Update
	err := c.Call(ctx, "getUpdates", map[string]any{
		"offset": offset, "limit": 50, "timeout": 30, "allowed_updates": []string{"message", "callback_query", "pre_checkout_query"},
	}, &updates)
	return updates, err
}

func (c *Client) AnswerCallback(ctx context.Context, id string) error {
	return c.Call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id}, nil)
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var user User
	err := c.Call(ctx, "getMe", map[string]any{}, &user)
	return user, err
}
