package mailcode

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"flashlarry-api/internal/siteconfig"
)

func Send(ctx context.Context, settings siteconfig.SMTP, recipient, code string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if e := settings.Validate(); e != nil {
		return e
	}
	a, e := mail.ParseAddress(recipient)
	if e != nil || a.Address != recipient || strings.ContainsAny(recipient, "\r\n") {
		return fmt.Errorf("invalid recipient")
	}
	password, e := settings.Secret()
	if e != nil {
		return e
	}
	tlsConfig := &tls.Config{ServerName: settings.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if settings.TLS == "tls" {
		conn, e = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", settings.Address())
	} else {
		conn, e = dialer.DialContext(ctx, "tcp", settings.Address())
	}
	if e != nil {
		return fmt.Errorf("SMTP connection failed")
	}
	defer conn.Close()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	c, e := smtp.NewClient(conn, settings.Host)
	if e != nil {
		return fmt.Errorf("SMTP greeting failed")
	}
	defer c.Close()
	if settings.TLS == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if e = c.StartTLS(tlsConfig); e != nil {
			return fmt.Errorf("SMTP TLS failed")
		}
	}
	if settings.Username != "" {
		if e = c.Auth(smtp.PlainAuth("", settings.Username, password, settings.Host)); e != nil {
			return fmt.Errorf("SMTP authentication failed")
		}
	}
	if e = c.Mail(settings.From); e != nil {
		return fmt.Errorf("SMTP sender rejected")
	}
	if e = c.Rcpt(recipient); e != nil {
		return fmt.Errorf("SMTP recipient rejected")
	}
	writer, e := c.Data()
	if e != nil {
		return fmt.Errorf("SMTP DATA failed")
	}
	message := "From: FlashLarry <" + settings.From + ">\r\nTo: " + recipient + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", "Код входа FlashLarry") + "\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nКод входа FlashLarry: " + code + "\r\nДействует 5 минут. Не сообщайте его другим людям.\r\n"
	if _, e = writer.Write([]byte(message)); e != nil {
		return fmt.Errorf("SMTP write failed")
	}
	if e = writer.Close(); e != nil {
		return fmt.Errorf("SMTP delivery rejected")
	}
	// DATA was accepted. A disconnect during QUIT must not invalidate a delivered code.
	_ = c.Quit()
	return nil
}
