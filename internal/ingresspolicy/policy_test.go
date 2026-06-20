package ingresspolicy

import (
	"net"
	"testing"
)

func TestSourceAllowedRequiresExplicitLoopbackCIDR(t *testing.T) {
	cidrs, err := ParseCIDRs([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatalf("parse CIDRs: %v", err)
	}

	if SourceAllowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 12345}, cidrs) {
		t.Fatal("loopback sources outside the configured CIDRs should be denied")
	}

	loopbackCIDRs, err := ParseCIDRs([]string{"127.0.0.0/8", "::1/128"})
	if err != nil {
		t.Fatalf("parse loopback CIDRs: %v", err)
	}
	if !SourceAllowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 12345}, loopbackCIDRs) {
		t.Fatal("IPv4 loopback should be allowed when 127.0.0.0/8 is configured")
	}
	if !SourceAllowed(&net.TCPAddr{IP: net.ParseIP("::1"), Port: 12345}, loopbackCIDRs) {
		t.Fatal("IPv6 loopback should be allowed when ::1/128 is configured")
	}
}
