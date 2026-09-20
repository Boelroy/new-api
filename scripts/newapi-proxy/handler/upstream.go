package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"newapi-proxy/config"
)

var (
	upstreamClientOnce sync.Once
	upstreamClient     *http.Client
)

// getUpstreamClient returns a shared http.Client that routes through
// UPSTREAM_PROXY when set, or makes direct connections otherwise.
func getUpstreamClient() *http.Client {
	upstreamClientOnce.Do(func() {
		transport := &http.Transport{}
		if config.UpstreamProxy != "" {
			if proxyURL, err := url.Parse(config.UpstreamProxy); err == nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		}
		upstreamClient = &http.Client{Transport: transport}
	})
	return upstreamClient
}

// do sends a request to the upstream new-api with the admin token injected,
// copies the response status + headers + body back to c, and returns.
func do(c *gin.Context, method, path string, body io.Reader) {
	upstreamURL := config.RemoteURL + path
	if q := c.Request.URL.RawQuery; q != "" {
		upstreamURL += "?" + q
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), method, upstreamURL, body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "upstream request build failed"})
		return
	}

	// Copy most request headers except hop-by-hop and auth (we inject our own).
	for k, vv := range c.Request.Header {
		k = http.CanonicalHeaderKey(k)
		switch k {
		case "Authorization", "Cookie", "Connection", "Te", "Trailers", "Transfer-Encoding", "Upgrade":
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+config.RemoteAdminToken)
	req.Header.Set("Content-Type", c.ContentType())

	resp, err := getUpstreamClient().Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "upstream unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Forward response headers.
	for k, vv := range resp.Header {
		k = http.CanonicalHeaderKey(k)
		if k == "Transfer-Encoding" {
			continue
		}
		for _, v := range vv {
			c.Header(k, v)
		}
	}

	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

// readBody drains and returns the request body, replacing c.Request.Body.
func readBody(c *gin.Context) ([]byte, error) {
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

// Passthrough forwards the request to upstream unchanged.
func Passthrough(c *gin.Context) {
	do(c, c.Request.Method, c.Request.URL.Path, c.Request.Body)
}

// stripPrefix removes a leading path component, e.g. "/api/v1/channels/42" → "/42".
func stripPrefix(path, prefix string) string {
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):]
	}
	return path
}
