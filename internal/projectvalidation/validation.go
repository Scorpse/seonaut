package projectvalidation

import (
	"errors"
	"net/url"
	"strings"

	"github.com/stjudewashere/seonaut/internal/models"
)

var (
	ErrProtocolNotSupported = errors.New("protocol not supported")
	ErrUserAgent            = errors.New("user agent string must not be empty")
)

// Prepare normalizes and validates a project independently of its HTTP transport.
func Prepare(project *models.Project) error {
	project.URL = strings.TrimSpace(project.URL)
	parsedURL, err := url.Parse(project.URL)
	if err != nil || parsedURL.Host == "" {
		return ErrProtocolNotSupported
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return ErrProtocolNotSupported
	}
	project.UserAgent = strings.TrimSpace(project.UserAgent)
	if project.UserAgent == "" {
		return ErrUserAgent
	}
	return nil
}
