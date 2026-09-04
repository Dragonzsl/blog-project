// Package clientip resolves the client address at the HTTP trust boundary.
//
// Forwarding headers are only meaningful when the directly connected peer is
// an explicitly configured reverse proxy.  Keeping this policy in one small
// package prevents authentication, public writes, and privacy statistics from
// silently adopting different interpretations of the same request.
package clientip

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const (
	Unknown    = "unknown"
	maxHops    = 16
	headerName = "X-Forwarded-For"
)

var ErrInvalidCIDR = errors.New("trusted proxy CIDR is invalid")

// Resolver accepts forwarding headers only from peers covered by its
// allowlist.  An empty allowlist intentionally means direct-peer-only mode.
type Resolver struct {
	trusted []netip.Prefix
}

func New(cidrs []string) (*Resolver, error) {
	resolver := &Resolver{}
	seen := make(map[string]struct{}, len(cidrs))
	if len(cidrs) > 32 {
		return nil, ErrInvalidCIDR
	}
	for _, raw := range cidrs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, ErrInvalidCIDR
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.IsValid() {
			return nil, ErrInvalidCIDR
		}
		address := prefix.Addr()
		bits := prefix.Bits()
		if address.Is4In6() {
			address = address.Unmap()
			bits -= 96
		}
		if bits < 0 {
			return nil, ErrInvalidCIDR
		}
		prefix = netip.PrefixFrom(address, bits).Masked()
		if !prefix.IsValid() {
			return nil, ErrInvalidCIDR
		}
		// An explicit default route would make every caller a trusted proxy and
		// is almost always an accidental header-spoofing configuration.
		if prefix.Bits() == 0 {
			return nil, ErrInvalidCIDR
		}
		key := prefix.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		resolver.trusted = append(resolver.trusted, prefix)
	}
	return resolver, nil
}

// DirectPeerOnly returns the safe default resolver.
func DirectPeerOnly() *Resolver { return &Resolver{} }

// Resolve returns a normalized IP string or Unknown when the direct peer is
// not a parseable address.  It never returns a value supplied by an
// untrusted forwarding header.
func (r *Resolver) Resolve(request *http.Request) string {
	if request == nil {
		return Unknown
	}
	direct, ok := parseAddr(request.RemoteAddr)
	if !ok {
		return Unknown
	}
	if r == nil || !r.trustedContains(direct) {
		return direct.String()
	}
	values := request.Header.Values(headerName)
	if len(values) != 1 {
		return direct.String()
	}
	parts := strings.Split(values[0], ",")
	if len(parts) == 0 || len(parts) > maxHops {
		return direct.String()
	}
	chain := make([]netip.Addr, len(parts))
	for index, part := range parts {
		address, valid := parseForwardedAddr(part)
		if !valid {
			return direct.String()
		}
		chain[index] = address
	}
	for index := len(chain) - 1; index >= 0; index-- {
		if !r.trustedContains(chain[index]) {
			return chain[index].String()
		}
	}
	// A chain made entirely of trusted hops has no independently verifiable
	// client.  The leftmost value is the conventional best-effort result while
	// still requiring every hop to be in the configured allowlist.
	return chain[0].String()
}

func (r *Resolver) trustedContains(address netip.Addr) bool {
	address = address.Unmap()
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	address, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil || !address.IsValid() || address.Zone() != "" {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func parseForwardedAddr(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "[]:") && strings.Contains(value, "]:") {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddr(value)
	if err != nil || !address.IsValid() || address.Zone() != "" {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}
