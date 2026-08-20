package services

import (
	"bytes"
	"testing"

	"github.com/stjudewashere/seonaut/internal/models"
)

type resourceExportFixture struct{}

func (resourceExportFixture) ExportLinks(*models.Crawl) <-chan *models.ExportLink {
	return closedChannel[*models.ExportLink]()
}
func (resourceExportFixture) ExportExternalLinks(*models.Crawl) <-chan *models.ExportLink {
	return closedChannel[*models.ExportLink]()
}
func (resourceExportFixture) ExportImages(*models.Crawl) <-chan *models.ExportImage {
	return valueChannel(&models.ExportImage{Origin: "https://a.example/one", Image: "https://a.example/logo.png", Alt: "Logo"})
}
func (resourceExportFixture) ExportScripts(*models.Crawl) <-chan *models.Script {
	return valueChannel(&models.Script{Origin: "https://a.example/one", Script: "https://a.example/app.js"})
}
func (resourceExportFixture) ExportStyles(*models.Crawl) <-chan *models.Style {
	return closedChannel[*models.Style]()
}
func (resourceExportFixture) ExportIframes(*models.Crawl) <-chan *models.Iframe {
	return closedChannel[*models.Iframe]()
}
func (resourceExportFixture) ExportAudios(*models.Crawl) <-chan *models.Audio {
	return closedChannel[*models.Audio]()
}
func (resourceExportFixture) ExportVideos(*models.Crawl) <-chan *models.ExportVideo {
	return valueChannel(&models.ExportVideo{Origin: "https://a.example/one", Video: "https://a.example/clip.mp4"})
}
func (resourceExportFixture) ExportHreflangs(*models.Crawl) <-chan *models.ExportHreflang {
	return closedChannel[*models.ExportHreflang]()
}
func (resourceExportFixture) ExportIssues(*models.Crawl) <-chan *models.ExportIssue {
	return closedChannel[*models.ExportIssue]()
}

func TestExportAllResourcesUsesOneStableCSVContract(t *testing.T) {
	exporter := NewExporter(resourceExportFixture{}, nil)
	var output bytes.Buffer
	exporter.ExportAllResources(&output, &models.Crawl{Id: 9})
	want := "Type,Origin,URL,Alt,Poster\n" +
		"image,https://a.example/one,https://a.example/logo.png,Logo,\n" +
		"script,https://a.example/one,https://a.example/app.js,,\n" +
		"video,https://a.example/one,https://a.example/clip.mp4,,\n"
	if output.String() != want {
		t.Fatalf("resources CSV:\n%s\nwant:\n%s", output.String(), want)
	}
}

func valueChannel[T any](value T) <-chan T {
	stream := make(chan T, 1)
	stream <- value
	close(stream)
	return stream
}

func closedChannel[T any]() <-chan T {
	stream := make(chan T)
	close(stream)
	return stream
}
