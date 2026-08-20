package api

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"testing"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, errors.New("host not found")
	}
	return addresses, nil
}

func TestTargetPolicyRejectsNonPublicTargets(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254",
		"100.100.100.200", "198.18.0.1", "0.0.0.0", "224.0.0.1", "::1", "fd00::1", "fe80::1", "ff02::1", "::",
	}
	for _, address := range blocked {
		t.Run(address, func(t *testing.T) {
			policy, err := NewTargetPolicy("production", nil, staticResolver{"blocked.example": {netip.MustParseAddr(address)}})
			if err != nil {
				t.Fatal(err)
			}
			target, _ := url.Parse("https://blocked.example/")
			if err := policy.ValidateURL(context.Background(), target); !errors.Is(err, ErrTargetForbidden) {
				t.Fatalf("ValidateURL error = %v, want ErrTargetForbidden", err)
			}
		})
	}
}

func TestTargetPolicyRejectsUnsafeSchemesCredentialsAndMixedDNS(t *testing.T) {
	policy, err := NewTargetPolicy("production", nil, staticResolver{
		"mixed.example": {netip.MustParseAddr("203.0.113.10"), netip.MustParseAddr("10.0.0.1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{"file:///etc/passwd", "ftp://mixed.example/", "https://user:pass@mixed.example/", "https://mixed.example/"}
	for _, raw := range tests {
		target, _ := url.Parse(raw)
		if err := policy.ValidateURL(context.Background(), target); !errors.Is(err, ErrTargetForbidden) {
			t.Fatalf("ValidateURL(%q) error = %v, want ErrTargetForbidden", raw, err)
		}
	}
}

func TestTargetPolicyAllowsPublicTargetsAndProductionBuildDisablesFixtureHosts(t *testing.T) {
	policy, err := NewTargetPolicy("production", nil, staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}})
	if err != nil {
		t.Fatal(err)
	}
	publicURL, _ := url.Parse("https://public.example/")
	if err := policy.ValidateURL(context.Background(), publicURL); err != nil {
		t.Fatalf("public target rejected: %v", err)
	}

	if _, err := NewTargetPolicy("production", []string{"fixture.test"}, staticResolver{}); !errors.Is(err, ErrTargetAllowlistDisabled) {
		t.Fatalf("production allowlist error = %v", err)
	}
	if !fixtureTargetAllowlistEnabled {
		if _, err := NewTargetPolicy("test", []string{"fixture.test"}, staticResolver{}); !errors.Is(err, ErrTargetAllowlistDisabled) {
			t.Fatalf("production build enabled fixture allowlist: %v", err)
		}
	}
}

func TestTargetPolicyRevalidatesEveryResolution(t *testing.T) {
	resolver := &sequenceResolver{answers: [][]netip.Addr{
		{netip.MustParseAddr("8.8.8.8")},
		{netip.MustParseAddr("127.0.0.1")},
	}}
	policy, err := NewTargetPolicy("production", nil, resolver)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://rebind.example/")
	if err := policy.ValidateURL(context.Background(), target); err != nil {
		t.Fatalf("first resolution rejected: %v", err)
	}
	if _, err := policy.DialContext(context.Background(), "tcp", "rebind.example:443"); !errors.Is(err, ErrTargetForbidden) {
		t.Fatalf("dial resolution error = %v, want ErrTargetForbidden", err)
	}
}

type sequenceResolver struct {
	answers [][]netip.Addr
	index   int
}

func (r *sequenceResolver) LookupNetIP(_ context.Context, _ string, _ string) ([]netip.Addr, error) {
	answer := r.answers[r.index]
	if r.index < len(r.answers)-1 {
		r.index++
	}
	return answer, nil
}
