package handler

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"newapi-proxy/config"
)

var (
	upstreamClientOnce sync.Once
	upstreamClient     *http.Client
)

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

// doUpstream forwards a request to the upstream new-api with the admin token
// injected, and copies the response back to w.
func doUpstream(w http.ResponseWriter, r *http.Request, method, path string, body io.Reader) {
	upstreamURL := config.RemoteURL + path
	if q := r.URL.RawQuery; q != "" {
		upstreamURL += "?" + q
	}

	req, err := http.NewRequestWithContext(r.Context(), method, upstreamURL, body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "upstream request build failed")
		return
	}

	for k, vv := range r.Header {
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
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := getUpstreamClient().Do(req)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "upstream unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		if http.CanonicalHeaderKey(k) == "Transfer-Encoding" {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// PassthroughHTTP forwards the request to upstream unchanged.
func PassthroughHTTP(w http.ResponseWriter, r *http.Request) {
	doUpstream(w, r, r.Method, r.URL.Path, r.Body)
}

// fetchUpstreamBody GETs path from upstream and returns the raw body bytes.
func fetchUpstreamBody(ctx *http.Request, path string) ([]byte, int, error) {
	upstreamURL := config.RemoteURL + path
	if q := ctx.URL.RawQuery; q != "" {
		upstreamURL += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	req.Header.Set("Authorization", "Bearer "+config.RemoteAdminToken)
	resp, err := getUpstreamClient().Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// stripPrefix removes a leading path prefix.
func stripPrefix(path, prefix string) string {
	if strings.HasPrefix(path, prefix) {
		return path[len(prefix):]
	}
	return path
}
