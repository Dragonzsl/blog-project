package clientip

import (
	"net/http"
	"testing"
)

func TestResolverUsesDirectPeerWithoutTrustedProxy(t *testing.T) {
	resolver, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	request.RemoteAddr = "10.0.0.2:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.10")
	if got := resolver.Resolve(request); got != "10.0.0.2" {
		t.Fatalf("client address=%q", got)
	}
}

func TestResolverFindsFirstUntrustedAddressFromRight(t *testing.T) {
	resolver, err := New([]string{"10.0.0.0/24", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	request.RemoteAddr = "10.0.0.2:443"
	request.Header.Set("X-Forwarded-For", "198.51.100.10, 192.0.2.10, 10.0.0.3")
	if got := resolver.Resolve(request); got != "198.51.100.10" {
		t.Fatalf("client address=%q", got)
	}
}

func TestResolverRejectsMalformedForwardingChain(t *testing.T) {
	resolver, err := New([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forwarded := range []string{"198.51.100.10:80", "198.51.100.10,,10.0.0.3", "198.51.100.10,not-an-ip", "fe80::1%eth0"} {
		request, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
		request.RemoteAddr = "10.0.0.2:443"
		request.Header.Set("X-Forwarded-For", forwarded)
		if got := resolver.Resolve(request); got != "10.0.0.2" {
			t.Fatalf("forwarded=%q address=%q", forwarded, got)
		}
	}
}

func TestResolverNormalizesIPv4MappedIPv6(t *testing.T) {
	resolver, err := New([]string{"::ffff:10.0.0.0/120"})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	request.RemoteAddr = "[::ffff:10.0.0.2]:443"
	request.Header.Set("X-Forwarded-For", "2001:db8::5")
	if got := resolver.Resolve(request); got != "2001:db8::5" {
		t.Fatalf("client address=%q", got)
	}
}

func TestResolverReturnsUnknownForInvalidPeer(t *testing.T) {
	resolver := DirectPeerOnly()
	request, _ := http.NewRequest(http.MethodGet, "http://example.test", nil)
	request.RemoteAddr = "proxy-name"
	if got := resolver.Resolve(request); got != Unknown {
		t.Fatalf("client address=%q", got)
	}
}
