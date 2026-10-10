package mail

import (
	"errors"
	"net"
	"net/textproto"
	"strings"
	"syscall"
	"testing"
)

func TestDeliveryErrorDiagnostics(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind string
		code int
	}{
		{"smtp response", &textproto.Error{Code: 535, Msg: "credentials and secret must not be logged"}, "smtp_response", 535},
		{"dns", &net.DNSError{Err: "no such host", Name: "private.example"}, "dns", 0},
		{"connection refused", syscall.ECONNREFUSED, "connection_refused", 0},
		{"unknown", errors.New("private secret"), "other", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deliveryError("auth", tt.err)
			var diagnostic *DeliveryError
			if !errors.As(got, &diagnostic) || diagnostic.Stage != "auth" || diagnostic.Kind != tt.kind || diagnostic.SMTPCode != tt.code {
				t.Fatalf("unexpected diagnostic: %#v", got)
			}
			for _, secret := range []string{"credentials", "secret", "private.example"} {
				if strings.Contains(got.Error(), secret) {
					t.Fatal("sensitive details exposed")
				}
			}
		})
	}
}
