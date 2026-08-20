package crawler

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"

	"github.com/mileusna/useragent"
)

type HTTPRequester interface {
	Do(req *http.Request) (*http.Response, error)
}

type BasicClient struct {
	Options *ClientOptions
	client  HTTPRequester
	uaName  string
}

type ClientOptions struct {
	UserAgent        string
	BasicAuthDomains []string
	AuthUser         string
	AuthPass         string
	TargetValidator  TargetValidator
	MaxResponseBytes int64
}

var ErrResponseTooLarge = errors.New("response exceeds configured byte limit")

type TargetValidator interface {
	ValidateURL(context.Context, *url.URL) error
}

func NewBasicClient(options *ClientOptions, client HTTPRequester) *BasicClient {
	bc := &BasicClient{
		Options: options,
		client:  client,
	}

	parsedUA := useragent.Parse(options.UserAgent)
	bc.uaName = parsedUA.Name

	return bc
}

// Makes a request with the method specified in the method parameter to the specified URL.
func (c *BasicClient) request(method, urlStr string) (*ClientResponse, error) {
	req, err := http.NewRequest(method, urlStr, nil)
	if err != nil {
		return nil, err
	}

	domain, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	if c.Options.AuthUser != "" && c.isBasicAuthDomain(domain.Host) {
		req.SetBasicAuth(c.Options.AuthUser, c.Options.AuthPass)
	}

	return c.do(req)
}

// Returns true if the domain exists in the BasicAutDomains slice.
func (c *BasicClient) isBasicAuthDomain(domain string) bool {
	for _, authDomain := range c.Options.BasicAuthDomains {
		if authDomain == domain {
			return true
		}
	}

	return false
}

// Makes a GET request to an URL and returns the http response or an error.
func (c *BasicClient) Get(urlStr string) (*ClientResponse, error) {
	return c.request(http.MethodGet, urlStr)
}

// Makes a HEAD request to an URL and returns the http response or an error.
func (c *BasicClient) Head(urlStr string) (*ClientResponse, error) {
	return c.request(http.MethodHead, urlStr)
}

// do executes a request and returns its response and error.
// It sets the client's User-Agent as well as the BasicAuth details if they are available.
func (c *BasicClient) do(req *http.Request) (*ClientResponse, error) {
	cr := &ClientResponse{}
	if c.Options.TargetValidator != nil {
		if err := c.Options.TargetValidator.ValidateURL(req.Context(), req.URL); err != nil {
			return nil, err
		}
	}

	start := time.Now()
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			// Time To First Byte in milliseconds
			cr.TTFB = int(math.Ceil(float64(time.Since(start) / time.Millisecond)))
		},
	}

	req.Header.Set("User-Agent", c.Options.UserAgent)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if c.Options.MaxResponseBytes > 0 && resp.Body != nil {
		if resp.ContentLength > c.Options.MaxResponseBytes {
			_ = resp.Body.Close()
			return nil, ErrResponseTooLarge
		}
		resp.Body = &limitedReadCloser{ReadCloser: resp.Body, remaining: c.Options.MaxResponseBytes}
	}

	cr.Response = resp

	return cr, nil
}

type limitedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *limitedReadCloser) Read(buffer []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(buffer)) > r.remaining {
			buffer = buffer[:r.remaining]
		}
		count, err := r.ReadCloser.Read(buffer)
		r.remaining -= int64(count)
		return count, err
	}
	var probe [1]byte
	count, err := r.ReadCloser.Read(probe[:])
	if count > 0 {
		return 0, ErrResponseTooLarge
	}
	return 0, err
}

// GetUAName returns the user-agent name for this client.
func (c *BasicClient) GetUAName() string {
	return c.uaName
}
