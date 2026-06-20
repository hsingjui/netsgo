package ingresspolicy

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

var allowAllSourceCIDRs = []string{"0.0.0.0/0", "::/0"}

type Policy struct {
	AllowedSourceCIDRs []string
	SourceCIDRs        []*net.IPNet
}

type config struct {
	AllowedSourceCIDRs []string `json:"allowed_source_cidrs"`
}

func AllowAllSourceCIDRs() []string {
	return append([]string(nil), allowAllSourceCIDRs...)
}

func NormalizeSourceCIDRs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("allowed_source_cidrs is required")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("allowed_source_cidrs contains an empty CIDR")
		}
		_, ipNet, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("allowed_source_cidrs contains invalid CIDR %q", value)
		}
		canonical := ipNet.String()
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	return out, nil
}

func Parse(values []string, allowMissing bool) (Policy, error) {
	if values == nil && allowMissing {
		values = AllowAllSourceCIDRs()
	}
	normalized, err := NormalizeSourceCIDRs(values)
	if err != nil {
		return Policy{}, err
	}
	cidrs, err := ParseCIDRs(normalized)
	if err != nil {
		return Policy{}, err
	}
	return Policy{AllowedSourceCIDRs: normalized, SourceCIDRs: cidrs}, nil
}

func Decode(raw json.RawMessage, allowMissing bool) (Policy, error) {
	var cfg config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Policy{}, err
		}
	}
	return Parse(cfg.AllowedSourceCIDRs, allowMissing)
}

func ParseCIDRs(values []string) ([]*net.IPNet, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("CIDR allowlist is required")
	}
	out := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, ipNet, err := net.ParseCIDR(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", value, err)
		}
		out = append(out, ipNet)
	}
	return out, nil
}

func SourceAllowed(addr net.Addr, cidrs []*net.IPNet) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}
