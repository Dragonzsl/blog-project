// Package netguard provides the narrow outbound HTTP and TCP boundary used by
// optional integrations.  It validates the configured URL and resolves the
// destination again for every connection so a DNS change cannot turn a public
// endpoint into a loopback or private-network request.
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidURL      = errors.New("external endpoint must be an absolute HTTP or HTTPS URL without credentials or fragment")
	ErrBlockedAddress  = errors.New("external endpoint resolves to a blocked address")
	ErrInvalidPort     = errors.New("external endpoint port is invalid")
	ErrResolverFailure = errors.New("external endpoint could not be resolved")
	ErrRedirect        = errors.New("external redirects are not allowed")
)

type Options struct {
	Timeout               time.Duration
	ConnectTimeout        time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	MaxIdleConns          int
	Resolver              *net.Resolver
}

func (o Options) normalized() Options {
	if o.Timeout <= 0 {
		o.Timeout = 15 * time.Second
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 5 * time.Second
	}
	if o.TLSHandshakeTimeout <= 0 {
		o.TLSHandshakeTimeout = 5 * time.Second
	}
	if o.ResponseHeaderTimeout <= 0 {
		o.ResponseHeaderTimeout = 10 * time.Second
	}
	if o.MaxIdleConns <= 0 || o.MaxIdleConns > 32 {
		o.MaxIdleConns = 8
	}
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	return o
}

// NewClient returns a bounded client which rejects redirects and private or
// local destinations.  Callers that need a test server should inject an
// explicit test client into their adapter instead of weakening this default.
func NewClient(configured ...Options) *http.Client {
	options := Options{}
	if len(configured) > 0 {
		options = configured[0]
	}
	options = options.normalized()
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           DialContext(options.Resolver, options.ConnectTimeout),
		TLSHandshakeTimeout:   options.TLSHandshakeTimeout,
		ResponseHeaderTimeout: options.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		MaxIdleConns:          options.MaxIdleConns,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   options.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
}

func IsPermanent(err error) bool {
	return errors.Is(err, ErrInvalidURL) || errors.Is(err, ErrInvalidPort) || errors.Is(err, ErrBlockedAddress) || errors.Is(err, ErrRedirect)
}

// ValidateURL performs the syntax portion of the outbound URL contract. DNS
// and address policy are deliberately checked by DialContext at connection
// time as well.
func ValidateURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" || len(parsed.String()) > 2048 {
		return nil, ErrInvalidURL
	}
	if parsed.Hostname() == "" {
		return nil, ErrInvalidURL
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidPort
		}
	}
	return parsed, nil
}

// DialContext resolves host on every call and dials only the validated IPs.
// It intentionally rejects the whole hostname when any answer is private;
// allowing a second public answer could otherwise make DNS rebinding policy
// dependent on resolver ordering.
func DialContext(resolver *net.Resolver, timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrInvalidURL
		}
		if portNumber, parseErr := strconv.Atoi(port); parseErr != nil || portNumber < 1 || portNumber > 65535 {
			return nil, ErrInvalidPort
		}
		ips, err := resolveHost(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if isBlocked(ip) {
				return nil, ErrBlockedAddress
			}
		}
		var lastErr error
		for _, ip := range ips {
			if network == "tcp4" && ip.To4() == nil {
				continue
			}
			if network == "tcp6" && ip.To4() != nil {
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrResolverFailure
	}
}

func resolveHost(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}
	if strings.TrimSpace(host) == "" || strings.Contains(host, "%") {
		return nil, ErrInvalidURL
	}
	addresses, err := resolver.LookupIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrResolverFailure
	}
	return addresses, nil
}

func isBlocked(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}
