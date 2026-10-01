package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/siteconfig"
	"flashlarry-api/internal/telegramapi"
	"github.com/redis/go-redis/v9"
)

var siteURL = common.PublicBaseURL()

type Bot struct {
	db       *sql.DB
	tg       *telegramapi.Client
	cache    *redis.Client
	client   *http.Client
	usersURL string
	token    string
}

func main() {
	token := common.Env("TELEGRAM_BOT_TOKEN", "")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bot := &Bot{tg: telegramapi.New(token, ""), cache: common.OpenRedis(), client: &http.Client{Timeout: 5 * time.Second}, usersURL: common.Env("USERS_URL", "http://127.0.0.1:8081"), token: token}
	bot.db = common.OpenDB()
	me, err := bot.tg.GetMe(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Telegram bot @%s started", me.Username)
	offset, _ := bot.cache.Get(ctx, "telegram-bot:offset").Int64()
	for ctx.Err() == nil {
		updates, err := bot.tg.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("poll failed: %v", err)
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		for _, update := range updates {
			if err := bot.handle(ctx, update); err != nil {
				log.Printf("update %d failed: %v", update.ID, err)
				break
			}
			offset = update.ID + 1
			if err := bot.cache.Set(ctx, "telegram-bot:offset", offset, 0).Err(); err != nil {
				log.Printf("offset save failed: %v", err)
			}
		}
	}
}

func menu() [][]telegramapi.Button {
	return [][]telegramapi.Button{
		{{Text: "✨ Зарегистрироваться", CallbackData: "register"}, {Text: "👤 Мой аккаунт", CallbackData: "account"}},
		{{Text: "⭐ FlashLarry Premium · 3 Stars", CallbackData: "premium"}},
		{{Text: "🌐 Открыть сайт", URL: siteURL + "/login"}},
	}
}

func cancelMenu() [][]telegramapi.Button {
	return [][]telegramapi.Button{{{Text: "Отмена", CallbackData: "cancel"}}}
}

func (b *Bot) stateKey(chatID int64) string {
	return "telegram-bot:state:" + strconv.FormatInt(chatID, 10)
}

func (b *Bot) setState(ctx context.Context, chatID int64, value string) error {
	return b.cache.Set(ctx, b.stateKey(chatID), value, 10*time.Minute).Err()
}

func (b *Bot) send(ctx context.Context, chatID int64, message string, buttons [][]telegramapi.Button) error {
	return b.tg.SendMessage(ctx, chatID, message, buttons)
}

func (b *Bot) handle(ctx context.Context, update telegramapi.Update) error {
	if b.db != nil {
		cfg, e := siteconfig.Load(ctx, b.db)
		if e != nil {
			return e
		}
		if cfg.AuthMode != "telegram" {
			if message := update.Message; message != nil && message.SuccessfulPayment != nil && message.Chat.Type == "private" && message.Chat.ID == message.From.ID {
				return b.recordDeferredPayment(ctx, message, message.SuccessfulPayment)
			}
			if q := update.PreCheckout; q != nil {
				return b.tg.Call(ctx, "answerPreCheckoutQuery", map[string]any{"pre_checkout_query_id": q.ID, "ok": false, "error_message": "Telegram-покупки отключены администратором."}, nil)
			}
			return nil
		}
	}
	if query := update.PreCheckout; query != nil {
		return b.preCheckout(ctx, query)
	}
	if query := update.Callback; query != nil {
		if query.Message.Chat.Type != "private" || query.Message.Chat.ID != query.From.ID {
			return nil
		}
		if err := b.tg.AnswerCallback(ctx, query.ID); err != nil {
			return err
		}
		return b.callback(ctx, query.From, query.Data)
	}
	message := update.Message
	if message == nil || message.Chat.Type != "private" || message.Chat.ID != message.From.ID {
		return nil
	}
	chatID := message.Chat.ID
	if payment := message.SuccessfulPayment; payment != nil {
		return b.successfulPayment(ctx, message, payment)
	}
	if strings.HasPrefix(message.Text, "/start") {
		return b.send(ctx, chatID, "FlashLarry ⚡️\nРегистрация проходит здесь. Для входа на сайт код придёт в этот чат.", menu())
	}
	state, err := b.cache.Get(ctx, b.stateKey(chatID)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	switch {
	case state == "register-email":
		email := strings.ToLower(strings.TrimSpace(message.Text))
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || !strings.HasSuffix(email, "@flashlarry.test") || len(email) > 254 {
			return b.send(ctx, chatID, "Отправьте тестовую почту вида name@flashlarry.test.", cancelMenu())
		}
		if err := b.setState(ctx, chatID, "confirm:"+email); err != nil {
			return err
		}
		return b.send(ctx, chatID, "Создать аккаунт с почтой "+email+"?", [][]telegramapi.Button{{{Text: "✅ Создать аккаунт", CallbackData: "confirm"}, {Text: "Отмена", CallbackData: "cancel"}}})
	default:
		return b.send(ctx, chatID, "Выберите действие кнопкой ниже.", menu())
	}
}

func (b *Bot) callback(ctx context.Context, from telegramapi.User, action string) error {
	chatID := from.ID
	switch action {
	case "register":
		var account map[string]any
		status, err := b.users(ctx, http.MethodGet, "/auth/telegram/account?chat_id="+strconv.FormatInt(chatID, 10), nil, &account)
		if err != nil {
			return err
		}
		if status == 200 {
			return b.send(ctx, chatID, "Вы уже зарегистрированы как @"+fmt.Sprint(account["screen_name"])+".", menu())
		}
		if status != 404 {
			return b.send(ctx, chatID, "Проверка аккаунта временно недоступна.", menu())
		}
		if err := b.setState(ctx, chatID, "register-email"); err != nil {
			return err
		}
		return b.send(ctx, chatID, "Напишите почту для нового аккаунта, например swag@flashlarry.test.", cancelMenu())
	case "confirm":
		state, err := b.cache.Get(ctx, b.stateKey(chatID)).Result()
		if err != nil || !strings.HasPrefix(state, "confirm:") {
			return b.send(ctx, chatID, "Подтверждение устарело. Начните регистрацию заново.", menu())
		}
		var account map[string]any
		status, err := b.users(ctx, http.MethodPost, "/auth/telegram/register", map[string]any{"chat_id": chatID, "email": strings.TrimPrefix(state, "confirm:"), "first_name": from.FirstName}, &account)
		if err != nil {
			return err
		}
		_ = b.cache.Del(ctx, b.stateKey(chatID)).Err()
		if status == 409 {
			return b.send(ctx, chatID, "Эта почта уже занята. Для регистрации выберите другую тестовую почту.", menu())
		}
		if status != 201 {
			return b.send(ctx, chatID, "Не удалось создать аккаунт. Попробуйте позже.", menu())
		}
		return b.send(ctx, chatID, "Готово! Ваш аккаунт: @"+fmt.Sprint(account["screen_name"])+". Для входа откройте сайт, введите почту и получите код здесь.", menu())
	case "account":
		var account map[string]any
		status, err := b.users(ctx, http.MethodGet, "/auth/telegram/account?chat_id="+strconv.FormatInt(chatID, 10), nil, &account)
		if err != nil {
			return err
		}
		if status == 200 {
			label := "Обычный аккаунт"
			if account["premium"] == true {
				label = "⭐ FlashLarry Premium · галочка активна"
			}
			return b.send(ctx, chatID, "Ваш аккаунт: @"+fmt.Sprint(account["screen_name"])+"\n"+label, [][]telegramapi.Button{{{Text: "Открыть профиль", URL: siteURL + "/" + url.PathEscape(fmt.Sprint(account["screen_name"]))}}, {{Text: "⭐ Premium · 3 Stars", CallbackData: "premium"}}, {{Text: "Меню", CallbackData: "menu"}}})
		}
		return b.send(ctx, chatID, "В этом Telegram пока нет аккаунта FlashLarry. Нажмите «Зарегистрироваться».", menu())
	case "premium":
		return b.sendPremiumInvoice(ctx, chatID)
	case "cancel", "menu":
		_ = b.cache.Del(ctx, b.stateKey(chatID)).Err()
		return b.send(ctx, chatID, "FlashLarry ⚡️", menu())
	default:
		return b.send(ctx, chatID, "Выберите действие кнопкой ниже.", menu())
	}
}

func (b *Bot) users(ctx context.Context, method, path string, input any, result any) (int, error) {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, b.usersURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Token", b.token)
	response, err := b.client.Do(req)
	if err != nil {
		return 0, errors.New("user service unavailable")
	}
	defer response.Body.Close()
	if result != nil {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			return 0, errors.New("invalid user service response")
		}
	}
	return response.StatusCode, nil
}
