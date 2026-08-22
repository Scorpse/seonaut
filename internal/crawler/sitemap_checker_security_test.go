package crawler

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type sitemapSecurityClient struct {
	get func(string) (*ClientResponse, error)
}

func (c sitemapSecurityClient) Get(target string) (*ClientResponse, error) { return c.get(target) }
func (c sitemapSecurityClient) Head(string) (*ClientResponse, error) {
	return nil, errors.New("unused")
}
func (c sitemapSecurityClient) GetUAName() string { return "test" }

func TestSitemapIndexUsesConfiguredCrawlerClient(t *testing.T) {
	requested := ""
	checker := NewSitemapChecker(sitemapSecurityClient{get: func(target string) (*ClientResponse, error) {
		requested = target
		body := `<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><sitemap><loc>https://example.com/child.xml</loc></sitemap></sitemapindex>`
		return &ClientResponse{Response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}}, nil
	}}, 10)
	got := checker.checkIndex("https://example.com/index.xml")
	if requested != "https://example.com/index.xml" || len(got) != 1 || got[0] != "https://example.com/child.xml" {
		t.Fatalf("requested=%q sitemaps=%v", requested, got)
	}
}

func TestRejectedSitemapDoesNotDeadlockParser(t *testing.T) {
	checker := NewSitemapChecker(sitemapSecurityClient{get: func(string) (*ClientResponse, error) {
		return nil, errors.New("target rejected")
	}}, 10)
	done := make(chan struct{})
	go func() {
		checker.ParseSitemaps([]string{"https://example.com/index.xml"}, func(string) {})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ParseSitemaps deadlocked after target rejection")
	}
}
