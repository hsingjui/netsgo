package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPTunnelSourceIPTrustsProxyHeadersOnlyFromTrustedPeers(t *testing.T) {
	t.Run("trusted proxy xff", func(t *testing.T) {
		s := New(0)
		s.TLS = &TLSConfig{Mode: TLSModeOff, TrustedProxies: []string{"10.0.0.0/8"}}
		req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
		req.RemoteAddr = "10.1.2.3:443"
		req.Header.Set("X-Forwarded-For", "203.0.113.44")

		if got := httpTunnelSourceIP(s, req); got != "203.0.113.44" {
			t.Fatalf("source IP = %q, want forwarded visitor IP", got)
		}
	})

	t.Run("untrusted proxy header ignored", func(t *testing.T) {
		s := New(0)
		s.TLS = &TLSConfig{Mode: TLSModeOff, TrustedProxies: []string{"10.0.0.0/8"}}
		req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
		req.RemoteAddr = "198.51.100.10:443"
		req.Header.Set("X-Forwarded-For", "203.0.113.44")

		if got := httpTunnelSourceIP(s, req); got != "198.51.100.10" {
			t.Fatalf("source IP = %q, want direct peer IP", got)
		}
	})

	t.Run("cloudflare-specific spoof headers ignored", func(t *testing.T) {
		s := New(0)
		s.TLS = &TLSConfig{Mode: TLSModeOff, TrustedProxies: []string{"10.0.0.0/8"}}
		req := httptest.NewRequest(http.MethodGet, "http://app.example.com/", nil)
		req.RemoteAddr = "10.1.2.3:443"
		req.Header.Set("CF-Connecting-IP", "203.0.113.44")
		req.Header.Set("True-Client-IP", "203.0.113.45")

		if got := httpTunnelSourceIP(s, req); got != "10.1.2.3" {
			t.Fatalf("source IP = %q, want trusted proxy direct IP when only CDN-specific headers are present", got)
		}
	})

	t.Run("trusted proxy works when server TLS remains enabled", func(t *testing.T) {
		s := New(0)
		s.TLS = &TLSConfig{Mode: TLSModeAuto, TrustedProxies: []string{"10.0.0.0/8"}}
		req := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
		req.RemoteAddr = "10.1.2.3:443"
		req.Header.Set("X-Forwarded-For", "203.0.113.44")

		if got := httpTunnelSourceIP(s, req); got != "203.0.113.44" {
			t.Fatalf("source IP = %q, want trusted proxy visitor IP", got)
		}
	})

	t.Run("forwarded chain selects nearest untrusted hop", func(t *testing.T) {
		s := New(0)
		s.TLS = &TLSConfig{Mode: TLSModeAuto, TrustedProxies: []string{"10.0.0.0/8"}}
		req := httptest.NewRequest(http.MethodGet, "https://app.example.com/", nil)
		req.RemoteAddr = "10.1.2.3:443"
		req.Header.Set("Forwarded", `for=203.0.113.44;proto=https, for=10.1.2.4`)

		if got := httpTunnelSourceIP(s, req); got != "203.0.113.44" {
			t.Fatalf("source IP = %q, want nearest untrusted Forwarded hop", got)
		}
	})
}
