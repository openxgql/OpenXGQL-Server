package main

import (
	"context"
	"encoding/json"
	"fmt"

	"flashlarry-api/internal/telegramapi"
)

// A checkout approved before a mode switch can finish after it. Keep the
// confirmed receipt for manual fulfillment without enabling bot purchases.
func (b *Bot) recordDeferredPayment(ctx context.Context, message *telegramapi.Message, p *telegramapi.SuccessfulPayment) error {
	user, valid := verifyPremiumPayload(b.token, p.InvoicePayload, message.From.ID)
	if !valid || p.Currency != "XTR" || p.TotalAmount != 3 || p.TelegramPaymentChargeID == "" {
		return nil
	}
	tx, e := b.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var alreadyGranted bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM premium_payments WHERE charge_id=$1)`, p.TelegramPaymentChargeID).Scan(&alreadyGranted); e != nil {
		return e
	}
	if alreadyGranted {
		return nil
	}
	result, e := tx.ExecContext(ctx, `INSERT INTO fl_pending_premium_payments(charge_id,user_id,telegram_id,currency,amount,invoice_payload) VALUES($1,$2::bigint,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, p.TelegramPaymentChargeID, user, message.From.ID, p.Currency, p.TotalAmount, p.InvoicePayload)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return nil
	}
	details := fmt.Sprintf("Подтверждённая оплата Premium: 3 Telegram Stars. Получена после отключения Telegram-входа. Выдайте Premium и галочку вручную в разделе пользователей. Квитанция: %s", p.TelegramPaymentChargeID)
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_reports(reporter_id,target_kind,target_id,reason,details) VALUES($1::bigint,'user',$1::text,'premium_payment', $2)`, user, details); e != nil {
		return e
	}
	data, _ := json.Marshal(map[string]any{"charge_id": p.TelegramPaymentChargeID, "amount": p.TotalAmount, "currency": p.Currency})
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES('telegram_bot','premium_pending',$1,$2)`, user, data); e != nil {
		return e
	}
	return tx.Commit()
}
