package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

type stringAddr string

func (a stringAddr) Network() string { return "tcp" }
func (a stringAddr) String() string  { return string(a) }

func httpTunnelSourceIP(s *Server, r *http.Request) string {
	if r == nil {
		return ""
	}
	directIP := remoteIPFromAddr(r.RemoteAddr)
	if s == nil || !s.trustHTTPTunnelSourceHeaders(directIP) {
		return normalizeHeaderIP(directIP)
	}
	trusted := func(ip string) bool {
		ip = normalizeHeaderIP(ip)
		return s.trustHTTPTunnelSourceHeaders(ip)
	}
	if ip := sourceIPFromForwardedHeader(headerValue(r.Header, "Forwarded"), directIP, trusted); ip != "" {
		return ip
	}
	if ip := sourceIPFromForwardedFor(headerValue(r.Header, "X-Forwarded-For"), directIP, trusted); ip != "" {
		return ip
	}
	if ip := normalizeHeaderIP(firstHeaderToken(headerValue(r.Header, "X-Real-IP"))); ip != "" {
		return ip
	}
	return normalizeHeaderIP(directIP)
}

func (s *Server) trustHTTPTunnelSourceHeaders(directIP string) bool {
	directIP = normalizeHeaderIP(directIP)
	return directIP != "" && (isLoopback(directIP) || (s != nil && s.TLS != nil && s.TLS.isConfiguredTrustedProxy(directIP)))
}

func headerValue(header http.Header, key string) string {
	if value := header.Get(key); value != "" {
		return value
	}
	for k, values := range header {
		if strings.EqualFold(k, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func sourceIPFromForwardedFor(raw string, directIP string, trusted func(string) bool) string {
	chain := parseHeaderIPList(raw)
	if len(chain) == 0 {
		return ""
	}
	if direct := normalizeHeaderIP(directIP); direct != "" {
		chain = append(chain, direct)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !trusted(chain[i]) {
			return chain[i]
		}
	}
	return chain[0]
}

func sourceIPFromForwardedHeader(raw string, directIP string, trusted func(string) bool) string {
	values := forwardedForValues(raw)
	if len(values) == 0 {
		return ""
	}
	chain := make([]string, 0, len(values))
	for _, value := range values {
		ip := normalizeHeaderIP(value)
		if ip == "" {
			return ""
		}
		chain = append(chain, ip)
	}
	if direct := normalizeHeaderIP(directIP); direct != "" {
		chain = append(chain, direct)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !trusted(chain[i]) {
			return chain[i]
		}
	}
	return chain[0]
}

func parseHeaderIPList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	ips := make([]string, 0, len(parts))
	for _, part := range parts {
		ip := normalizeHeaderIP(part)
		if ip == "" {
			return nil
		}
		ips = append(ips, ip)
	}
	return ips
}

func forwardedForValues(raw string) []string {
	if raw == "" {
		return nil
	}
	var values []string
	for _, entry := range strings.Split(raw, ",") {
		for _, param := range strings.Split(entry, ";") {
			param = strings.TrimSpace(param)
			if len(param) < 4 || !strings.EqualFold(param[:4], "for=") {
				continue
			}
			values = append(values, strings.Trim(param[4:], `"`))
			break
		}
	}
	return values
}

func normalizeHeaderIP(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"`)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "_") || strings.EqualFold(value, "unknown") {
		return ""
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	} else if strings.Count(value, ":") == 1 && !strings.Contains(value, "]") {
		host, port, ok := strings.Cut(value, ":")
		if ok && port != "" {
			if _, err := strconv.Atoi(port); err == nil {
				value = host
			}
		}
	}
	value = strings.Trim(value, "[]")
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}
