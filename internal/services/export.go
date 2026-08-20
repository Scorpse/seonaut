package services

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/stjudewashere/seonaut/internal/models"
)

type (
	ExportRepository interface {
		ExportLinks(*models.Crawl) <-chan *models.ExportLink
		ExportExternalLinks(*models.Crawl) <-chan *models.ExportLink
		ExportImages(crawl *models.Crawl) <-chan *models.ExportImage
		ExportScripts(crawl *models.Crawl) <-chan *models.Script
		ExportStyles(crawl *models.Crawl) <-chan *models.Style
		ExportIframes(crawl *models.Crawl) <-chan *models.Iframe
		ExportAudios(crawl *models.Crawl) <-chan *models.Audio
		ExportVideos(crawl *models.Crawl) <-chan *models.ExportVideo
		ExportHreflangs(crawl *models.Crawl) <-chan *models.ExportHreflang
		ExportIssues(crawl *models.Crawl) <-chan *models.ExportIssue
	}

	ExportTranslator interface {
		Trans(lang, s string, args ...interface{}) string
	}
	Exporter struct {
		repository ExportRepository
		translator ExportTranslator
	}
)

func NewExporter(r ExportRepository, t ExportTranslator) *Exporter {
	return &Exporter{
		repository: r,
		translator: t,
	}
}

// Export internal links as a CSV file
func (e *Exporter) ExportLinks(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Destination",
		"Text",
	})

	lStream := e.repository.ExportLinks(crawl)

	for v := range lStream {
		w.Write([]string{
			v.Origin,
			v.Destination,
			v.Text,
		})
	}

	w.Flush()
}

// Export internal links as a CSV file
func (e *Exporter) ExportExternalLinks(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Destination",
		"Text",
	})

	lStream := e.repository.ExportExternalLinks(crawl)

	for v := range lStream {
		w.Write([]string{
			v.Origin,
			v.Destination,
			v.Text,
		})
	}

	w.Flush()
}

// Export all images as a CSV file
func (e *Exporter) ExportImages(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Image URL",
		"Alt",
	})

	iStream := e.repository.ExportImages(crawl)

	for v := range iStream {
		w.Write([]string{
			v.Origin,
			v.Image,
			v.Alt,
		})
	}

	w.Flush()
}

// Export all scripts as a CSV file
func (e *Exporter) ExportScripts(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Script URL",
	})

	sStream := e.repository.ExportScripts(crawl)

	for v := range sStream {
		w.Write([]string{
			v.Origin,
			v.Script,
		})
	}

	w.Flush()
}

// Export all CSS styles as a CSV file
func (e *Exporter) ExportStyles(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Style URL",
	})

	sStream := e.repository.ExportStyles(crawl)

	for v := range sStream {
		w.Write([]string{
			v.Origin,
			v.Style,
		})
	}

	w.Flush()
}

// Export all CSS styles as a CSV file
func (e *Exporter) ExportIframes(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Iframe URL",
	})

	vStream := e.repository.ExportIframes(crawl)

	for v := range vStream {
		w.Write([]string{
			v.Origin,
			v.Iframe,
		})
	}

	w.Flush()
}

// Export all audio as a CSV file
func (e *Exporter) ExportAudios(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Audio URL",
	})

	vStream := e.repository.ExportAudios(crawl)

	for v := range vStream {
		w.Write([]string{
			v.Origin,
			v.Audio,
		})
	}

	w.Flush()
}

// Export all video as a CSV file
func (e *Exporter) ExportVideos(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Video URL",
	})

	vStream := e.repository.ExportVideos(crawl)

	for v := range vStream {
		w.Write([]string{
			v.Origin,
			v.Video,
		})
	}

	w.Flush()
}

// ExportAllResources exports crawl media and embedded assets under one stable API CSV contract.
func (e *Exporter) ExportAllResources(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)
	_ = w.Write([]string{"Type", "Origin", "URL", "Alt", "Poster"})
	for v := range e.repository.ExportImages(crawl) {
		_ = w.Write([]string{"image", v.Origin, v.Image, v.Alt, ""})
	}
	for v := range e.repository.ExportScripts(crawl) {
		_ = w.Write([]string{"script", v.Origin, v.Script, "", ""})
	}
	for v := range e.repository.ExportStyles(crawl) {
		_ = w.Write([]string{"style", v.Origin, v.Style, "", ""})
	}
	for v := range e.repository.ExportIframes(crawl) {
		_ = w.Write([]string{"iframe", v.Origin, v.Iframe, "", ""})
	}
	for v := range e.repository.ExportAudios(crawl) {
		_ = w.Write([]string{"audio", v.Origin, v.Audio, "", ""})
	}
	for v := range e.repository.ExportVideos(crawl) {
		_ = w.Write([]string{"video", v.Origin, v.Video, "", v.Poster})
	}
	w.Flush()
}

// Export all hreflangs as a CSV file
func (e *Exporter) ExportHreflangs(f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write([]string{
		"Origin",
		"Origin Language",
		"Hreflang",
		"Hreflang Language",
	})

	vStream := e.repository.ExportHreflangs(crawl)

	for v := range vStream {
		w.Write([]string{
			v.Origin,
			v.OriginLang,
			v.Hreflang,
			v.HreflangLang,
		})
	}

	w.Flush()
}

// Export all issues as a CSV file. It includes the URL, issue type and priority
func (e *Exporter) ExportAllIssues(lang string, f io.Writer, crawl *models.Crawl) {
	w := csv.NewWriter(f)

	w.Write(e.issueCSVHeader())

	vStream := e.repository.ExportIssues(crawl)

	for v := range vStream {
		w.Write(e.issueCSVRow(lang, v))
	}

	w.Flush()
}

func (e *Exporter) issueCSVHeader() []string {
	return []string{"URL", "Issue Type", "Priority"}
}

func (e *Exporter) issueCSVRow(lang string, issue *models.ExportIssue) []string {
	priority := "Warning"
	switch issue.Priority {
	case Critical:
		priority = "Critical"
	case Alert:
		priority = "Alert"
	}
	return []string{issue.Url, e.translator.Trans(lang, issue.Type), priority}
}

// ExportPageReports exports the pagereport data for all the pageReports that are received
// in the prStream channel. This export method is used to export all pageReports of crawl
// or only the pageReports with specific issues in a crawl.
func (e *Exporter) ExportPageReports(f io.Writer, prStream <-chan *models.PageReport) {
	writer := csv.NewWriter(f)
	writer.Write(e.pageReportCSVHeader())

	for r := range prStream {
		writer.Write(e.pageReportCSVRow(r))

		writer.Flush()
	}
}

func (e *Exporter) pageReportCSVHeader() []string {
	return []string{"Status Code", "URL", "Redirect URL", "Content Type", "Canonical", "Lang", "Title", "Title Length", "Description", "Description Length", "Robots", "Header 1", "Header 2", "Size", "Nº of words", "Depth", "TTFB"}
}

func (e *Exporter) pageReportCSVRow(report *models.PageReport) []string {
	return []string{
		fmt.Sprintf("%d", report.StatusCode), report.URL, report.RedirectURL, report.ContentType, report.Canonical, report.Lang,
		report.Title, fmt.Sprint(utf8.RuneCount([]byte(report.Title))), report.Description, fmt.Sprint(utf8.RuneCount([]byte(report.Description))),
		report.Robots, report.H1, report.H2, fmt.Sprintf("%.1f KB", e.byteToKByte(report.Size)), strconv.Itoa(report.Words), fmt.Sprintf("%d", report.Depth), fmt.Sprintf("%d ms", report.TTFB),
	}
}

// byteToKByte is a helper function to transform bytes to KBytes.
// It is used to format the pagereport size in the exported csv file.
func (e *Exporter) byteToKByte(b int64) float64 {
	v := b / (1 << 10)
	r := b % (1 << 10)

	return float64(v) + float64(r)/float64(1<<10)
}
