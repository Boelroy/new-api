package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Suppliers paste whatever the callback page shows; every shape must yield
// the bare code plus any embedded state.
func TestCCMaxParseCode(t *testing.T) {
	cases := []struct {
		name, raw, code, state string
	}{
		{"bare code", "  abc123  ", "abc123", ""},
		{"code#state", "abc123#st1", "abc123", "st1"},
		{"callback query", "http://localhost:54545/callback?code=abc123&state=st1", "abc123", "st1"},
		{"callback fragment", "http://localhost:54545/callback#code=abc123&state=st1", "abc123", "st1"},
		{"query string", "?code=abc123&state=st1", "abc123", "st1"},
		{"code param carrying #state", "https://x/cb?code=abc123%23st2", "abc123", "st2"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state := ccmaxParseCode(tc.raw)
			assert.Equal(t, tc.code, code)
			assert.Equal(t, tc.state, state)
		})
	}
}

// Suppliers either paste a proxy URL or fill the fields; both must yield the
// same canonical URL, and anything that is not a usable socks5 is rejected.
func TestCCMaxProxyURL(t *testing.T) {
	cases := []struct {
		name string
		in   ccmaxProxyInput
		want string
		ok   bool
	}{
		{"fields", ccmaxProxyInput{Host: "1.2.3.4", Port: "1080", Username: "u", Password: "p@ss"}, "socks5://u:p%40ss@1.2.3.4:1080", true},
		{"fields no auth", ccmaxProxyInput{Host: "1.2.3.4", Port: "1080"}, "socks5://1.2.3.4:1080", true},
		{"url", ccmaxProxyInput{URL: " socks5://u:p@1.2.3.4:1080 "}, "socks5://u:p@1.2.3.4:1080", true},
		{"url keeps socks5h", ccmaxProxyInput{URL: "socks5h://u:p@proxy.example.com:1080"}, "socks5h://u:p@proxy.example.com:1080", true},
		{"url without scheme", ccmaxProxyInput{URL: "u:p@1.2.3.4:1080"}, "socks5://u:p@1.2.3.4:1080", true},
		{"url wins over fields", ccmaxProxyInput{URL: "socks5://1.2.3.4:1080", Host: "5.6.7.8", Port: "9"}, "socks5://1.2.3.4:1080", true},
		{"http rejected", ccmaxProxyInput{URL: "http://1.2.3.4:8080"}, "", false},
		{"missing port", ccmaxProxyInput{URL: "socks5://1.2.3.4"}, "", false},
		{"bad port", ccmaxProxyInput{Host: "1.2.3.4", Port: "70000"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.proxyURL()
			if !tc.ok {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
