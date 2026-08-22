package services

import (
	"context"
	"net/url"
	"testing"

	"github.com/stjudewashere/seonaut/internal/config"
	"github.com/stjudewashere/seonaut/internal/crawler"
	"github.com/stjudewashere/seonaut/internal/models"
)

type crawlerSecurityValidator struct{}

func (crawlerSecurityValidator) ValidateURL(context.Context, *url.URL) error { return nil }

func TestStrictTargetPolicyAppliesOnlyToAPICrawls(t *testing.T) {
	service := NewCrawlerService(nil, CrawlerServicesContainer{
		Config:          &config.CrawlerConfig{Agent: "test"},
		TargetValidator: crawlerSecurityValidator{},
	})
	target, _ := url.Parse("http://127.0.0.1/")
	project := models.Project{Id: 1, URL: target.String(), UserAgent: "test"}

	uiCrawler, err := service.addCrawler(target, &project, &models.BasicAuth{}, false)
	if err != nil {
		t.Fatal(err)
	}
	uiClient := uiCrawler.Client.(*crawler.BasicClient)
	if uiClient.Options.TargetValidator != nil {
		t.Fatal("UI crawler unexpectedly received the API-only target policy")
	}
	if uiClient.Options.MaxResponseBytes != 0 {
		t.Fatalf("UI crawler unexpectedly received API byte cap: %d", uiClient.Options.MaxResponseBytes)
	}
	service.removeCrawler(&project)

	apiCrawler, err := service.addCrawler(target, &project, &models.BasicAuth{}, true)
	if err != nil {
		t.Fatal(err)
	}
	apiClient := apiCrawler.Client.(*crawler.BasicClient)
	if apiClient.Options.TargetValidator == nil {
		t.Fatal("API crawler did not receive the strict target policy")
	}
	if apiClient.Options.MaxResponseBytes != maxBodySize {
		t.Fatalf("API byte cap=%d want=%d", apiClient.Options.MaxResponseBytes, maxBodySize)
	}
	service.removeCrawler(&project)
}
