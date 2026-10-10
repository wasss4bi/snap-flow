package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"syscall"
	"time"
)

// DeliveryError contains only safe, structured diagnostics for logs.
type DeliveryError struct {
	Stage    string
	Kind     string
	SMTPCode int
}

func (e *DeliveryError) Error() string { return "SMTP " + e.Stage + ": " + e.Kind }

func deliveryError(stage string, err error) error {
	e := &DeliveryError{Stage: stage, Kind: "other"}
	var response *textproto.Error
	var dns *net.DNSError
	var netErr net.Error
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var certificateInvalid x509.CertificateInvalidError
	switch {
	case errors.As(err, &response):
		e.Kind, e.SMTPCode = "smtp_response", response.Code
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		e.Kind = "timeout"
	case errors.As(err, &dns):
		e.Kind = "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Kind = "connection_refused"
	case errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH):
		e.Kind = "network_unreachable"
	case errors.As(err, &unknownAuthority) || errors.As(err, &hostnameError) || errors.As(err, &certificateInvalid):
		e.Kind = "certificate_invalid"
	case errors.Is(err, context.Canceled):
		e.Kind = "cancelled"
	}
	return e
}

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
		return &DeliveryError{Stage: "sender_validation", Kind: "invalid_address"}
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil || recipient.Address != to {
		return &DeliveryError{Stage: "recipient_validation", Kind: "invalid_address"}
	}
	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.Address, recipient.Address, subject, body))
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, s.Port))
	if err != nil {
		return deliveryError("connect", err)
	}
	defer func() { _ = conn.Close() }() // Best effort; SMTP errors are reported by the operations below.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return deliveryError("greeting", err)
	}
	defer func() { _ = client.Close() }() // Quit reports delivery errors; Close is fallback cleanup.
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err = client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return deliveryError("starttls", err)
		}
	} else if s.RequireTLS {
		return &DeliveryError{Stage: "starttls", Kind: "not_supported"}
	}
	if s.User != "" {
		if err = client.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return deliveryError("auth", err)
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return deliveryError("mail_from", err)
	}
	if err = client.Rcpt(recipient.Address); err != nil {
		return deliveryError("rcpt_to", err)
	}
	w, err := client.Data()
	if err != nil {
		return deliveryError("data", err)
	}
	if _, err = w.Write(msg); err != nil {
		return deliveryError("write", err)
	}
	if err = w.Close(); err != nil {
		return deliveryError("data_close", err)
	}
	if err := client.Quit(); err != nil {
		return deliveryError("quit", err)
	}
	return nil
}
