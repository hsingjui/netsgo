package server

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// WebhookSettings controls server-wide outbound Webhook policy.
type WebhookSettings struct {
	// AllowPrivateTargets permits Webhook delivery to loopback, RFC1918,
	// link-local, and other non-public addresses. It is disabled by default so
	// a user cannot turn the Server into an SSRF pivot into its own network.
	AllowPrivateTargets bool `json:"allow_private_targets"`
	// DailyDeliveryCap bounds how many test and replay deliveries a single
	// user may enqueue per rolling 24h window. Event-driven deliveries are not
	// counted, so legitimate activity traffic is never dropped.
	DailyDeliveryCap int `json:"daily_delivery_cap"`
}

const (
	defaultWebhookDailyDeliveryCap = 50
	maxWebhookDailyDeliveryCap     = 10000
)

func defaultWebhookSettings() WebhookSettings {
	return WebhookSettings{AllowPrivateTargets: false, DailyDeliveryCap: defaultWebhookDailyDeliveryCap}
}

var errInvalidWebhookSettings = errors.New("webhook daily delivery cap must be between 1 and " + strconv.Itoa(maxWebhookDailyDeliveryCap))

func validateWebhookSettings(settings WebhookSettings) error {
	if settings.DailyDeliveryCap < 1 || settings.DailyDeliveryCap > maxWebhookDailyDeliveryCap {
		return errInvalidWebhookSettings
	}
	return nil
}

// webhookAddressAllowed reports whether an IP may receive Webhook traffic when
// private targets are disabled. Anything outside the routable public internet
// — loopback, RFC1918, shared/CGNAT, link-local, multicast, unspecified,
// benchmarking, documentation, and other IANA special-purpose ranges — is
// rejected.
func webhookAddressAllowed(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		for _, network := range webhookBlockedIPv4Ranges {
			if network.Contains(v4) {
				return false
			}
		}
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, network := range webhookBlockedIPv6Ranges {
		if network.Contains(ip) {
			return false
		}
	}
	return true
}

var (
	webhookBlockedIPv4Ranges = mustWebhookCIDRs(
		"0.0.0.0/8",       // "this network"
		"10.0.0.0/8",      // RFC1918 private
		"100.64.0.0/10",   // CGNAT shared address space
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local (incl. cloud metadata)
		"172.16.0.0/12",   // RFC1918 private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1 (documentation)
		"192.88.99.0/24",  // 6to4 relay anycast (deprecated)
		"192.168.0.0/16",  // RFC1918 private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2 (documentation)
		"203.0.113.0/24",  // TEST-NET-3 (documentation)
		"240.0.0.0/4",     // reserved (incl. 255.255.255.255 broadcast)
	)
	webhookBlockedIPv6Ranges = mustWebhookCIDRs(
		"::/128",         // unspecified
		"::1/128",        // loopback
		"64:ff9b::/96",   // well-known NAT64 prefix
		"64:ff9b:1::/48", // local-use NAT64
		"100::/64",       // discard-only
		"2001:db8::/32",  // documentation
		"2002::/16",      // 6to4, teredo-adjacent
		"fc00::/7",       // unique-local
		"fe80::/10",      // link-local
		"ff00::/8",       // multicast
	)
)

func mustWebhookCIDRs(values ...string) []*net.IPNet {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic("webhook policy: invalid blocked CIDR " + value)
		}
		result = append(result, network)
	}
	return result
}

// urlHostname normalizes a URL host to a bare hostname: port stripped, IPv6
// brackets removed. Callers pass url.URL.Hostname() output (already bare), so
// this only defends against future callers passing host:port forms. Unlike
// url.Parse it must never fail on a syntactically odd host, because treating
// unparsable hosts as empty would make webhookHostBlocked reject everything.
func urlHostname(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		return hostname
	}
	return strings.Trim(host, "[]")
}

func webhookHostBlocked(host string) bool {
	host = urlHostname(host)
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return !webhookAddressAllowed(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if !webhookAddressAllowed(ip) {
			return true
		}
	}
	return false
}

// validWebhookHeaderValue mirrors net/http's header-value rules closely enough
// for rendered Webhook headers: printable bytes plus tab, without CR/LF/NUL.
func validWebhookHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
