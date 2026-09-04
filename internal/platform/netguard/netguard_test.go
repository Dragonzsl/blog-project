package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestValidateURLRejectsCredentialsAndFragments(t *testing.T) {
	for _, value := range []string{"http://user:pass@example.com/hook", "https://example.com/hook#fragment", "file:///tmp/hook", "http://example.com:70000"} {
		if _, err := ValidateURL(value); err == nil {
			t.Fatalf("URL %q accepted", value)
		}
	}
}

func TestDialContextRejectsLoopbackLiteral(t *testing.T) {
	dial := DialContext(net.DefaultResolver, time.Second)
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:80"); err != ErrBlockedAddress {
		t.Fatalf("error=%v, want %v", err, ErrBlockedAddress)
	}
}

func TestClientRejectsRedirects(t *testing.T) {
	client := NewClient()
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect error=%v, want %v", err, ErrRedirect)
	}
}
