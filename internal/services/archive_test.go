package services

import (
	"path/filepath"
	"testing"

	"github.com/stjudewashere/seonaut/internal/models"
)

func TestAPIArchiveIsCreatedAtCrawlOwnedPath(t *testing.T) {
	service := NewArchiveService(t.TempDir())
	project := &models.Project{Id: 4, URL: "https://a.example/path"}
	writer, publish, err := service.GetAPIArchiveWriter(project, "crawl-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAPIArchiveFilePath(project, "crawl-a"); err == nil {
		t.Fatal("partial archive was published")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publish(); err != nil {
		t.Fatal(err)
	}
	path, err := service.GetAPIArchiveFilePath(project, "crawl-a")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "a.example.wacz" || filepath.Base(filepath.Dir(path)) != "crawl-a" {
		t.Fatalf("path=%s", path)
	}
	if err := service.DeleteAPIArchive("crawl-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetAPIArchiveFilePath(project, "crawl-a"); err == nil {
		t.Fatal("archive directory was not removed")
	}
}

func TestAPIArchiveRejectsPathTraversal(t *testing.T) {
	service := NewArchiveService(t.TempDir())
	if _, _, err := service.GetAPIArchiveWriter(&models.Project{Host: "a.example"}, "../escape"); err == nil {
		t.Fatal("expected invalid crawl ID")
	}
}
