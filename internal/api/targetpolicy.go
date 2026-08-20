package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var (
	ErrTargetForbidden         = errors.New("target is not allowed")
	ErrTargetAllowlistDisabled = errors.New("target allowlist is available only in tests")
)

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type TargetValidator interface {
	ValidateURL(context.Context, *url.URL) error
}

type TargetPolicy struct {
	resolver   IPResolver
	allowHosts map[string]struct{}
	dialer     net.Dialer
}

func NewTargetPolicy(environment string, allowHosts []string, resolver IPResolver) (*TargetPolicy, error) {
	if len(allowHosts) > 0 && environment != "test" {
		return nil, ErrTargetAllowlistDisabled
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	policy := &TargetPolicy{
		resolver:   resolver,
		allowHosts: make(map[string]struct{}, len(allowHosts)),
		dialer:     net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
	for _, host := range allowHosts {
		host = normalizeTargetHost(host)
		if host != "" {
			policy.allowHosts[host] = struct{}{}
		}
	}
	return policy, nil
}

func (p *TargetPolicy) ValidateURL(ctx context.Context, target *url.URL) error {
	if p == nil || target == nil || target.Host == "" || target.User != nil || target.Scheme != "http" && target.Scheme != "https" {
		return ErrTargetForbidden
	}
	host := normalizeTargetHost(target.Hostname())
	if host == "" {
		return ErrTargetForbidden
	}
	_, err := p.resolve(ctx, host)
	return err
}

// DialContext re-resolves and validates the destination immediately before the
// connection, then dials the validated address rather than the hostname.
func (p *TargetPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse target address: %w", ErrTargetForbidden)
	}
	addresses, err := p.resolve(ctx, normalizeTargetHost(host))
	if err != nil {
		return nil, err
	}
	var dialErr error
	for _, target := range addresses {
		connection, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(target.String(), port))
		if err == nil {
			return connection, nil
		}
		dialErr = errors.Join(dialErr, err)
	}
	return nil, dialErr
}

func (p *TargetPolicy) Transport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = p.DialContext
	return transport
}

func (p *TargetPolicy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if _, allowed := p.allowHosts[host]; allowed {
		if parsed, err := netip.ParseAddr(host); err == nil {
			return []netip.Addr{parsed}, nil
		}
		return p.resolver.LookupNetIP(ctx, "ip", host)
	}
	var addresses []netip.Addr
	if parsed, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{parsed}
	} else {
		var err error
		addresses, err = p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve target: %w", err)
		}
	}
	if len(addresses) == 0 {
		return nil, ErrTargetForbidden
	}
	for _, address := range addresses {
		if !publicTargetAddress(address) {
			return nil, ErrTargetForbidden
		}
	}
	return addresses, nil
}

func publicTargetAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast() && !address.IsUnspecified()
}

func normalizeTargetHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}
