//go:build fixture

package api

import (
	"context"
	"net/netip"
	"net/url"
	"testing"
)

func TestFixtureBuildAllowsExplicitTestHost(t *testing.T) {
	policy, err := NewTargetPolicy("test", []string{"fixture.test"}, staticResolver{"fixture.test": {netip.MustParseAddr("127.0.0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("http://fixture.test:8080/")
	if err := policy.ValidateURL(context.Background(), target); err != nil {
		t.Fatalf("fixture target rejected: %v", err)
	}
}
