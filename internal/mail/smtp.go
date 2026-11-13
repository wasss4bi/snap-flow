package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"

	"time"
)

type Sender interface {
	SendVerification(context.Context, string, string) error
}
type SMTP struct {
	Host, Port, User, Password, From, BaseURL string
	RequireTLS                                bool
}

func (s *SMTP) SendVerification(ctx context.Context, to, token string) error {
	return s.send(ctx, to, "Confirm your SnapFlow email", fmt.Sprintf("Your confirmation code: %s. It expires in 10 minutes.", token))
}
func (s *SMTP) SendExpiry(ctx context.Context, to string, deadline time.Time) error {
	return s.send(ctx, to, "Your SnapFlow storage access expired", fmt.Sprintf("Your storage access has expired. Your photographs are scheduled for deletion at %s UTC unless your access is extended.", deadline.UTC().Format("2006-01-02 15:04:05")))
}
func (s *SMTP) send(ctx context.Context, to, subject, body string) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil || from.Address != s.From {
		return errors.New("invalid SMTP sender")
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil || recipient.Address != to {
		return errors.New("invalid recipient")
	}
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.Address, recipient.Address, subject, body))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, s.Port))
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return errors.New("SMTP negotiation failed")
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return errors.New("SMTP TLS failed")
		}
	} else if s.RequireTLS {
		return errors.New("SMTP server does not support TLS")
	}
	if s.User != "" {
		if err = client.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender rejected")
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return errors.New("SMTP recipient rejected")
	}
	w, err := client.Data()
	if err != nil {
		return errors.New("SMTP message rejected")
	}
	if _, err = w.Write(msg); err != nil {
		return errors.New("SMTP write failed")
	}
	if err = w.Close(); err != nil {
		return errors.New("SMTP delivery failed")
	}
	return client.Quit()
}
