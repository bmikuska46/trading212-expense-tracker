package httpserver

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestClientIPOnlyTrustsConfiguredProxyChains(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("2001:db8:1::/64")}
	for _, test := range []struct {
		name, peer, forwarded, expected string
		trust                           bool
	}{
		{"direct IPv4", "192.0.2.1:1234", "198.51.100.1", "192.0.2.1", false},
		{"untrusted peer", "192.0.2.1:1234", "198.51.100.1", "192.0.2.1", true},
		{"one trusted proxy", "10.0.0.2:1234", "198.51.100.1", "198.51.100.1", true},
		{"multiple proxies", "10.0.0.2:1234", "198.51.100.1, 10.0.0.3", "198.51.100.1", true},
		{"spoofed leftmost", "10.0.0.2:1234", "203.0.113.50, 198.51.100.1", "198.51.100.1", true},
		{"untrusted intermediate", "10.0.0.2:1234", "203.0.113.50, 198.51.100.1, 10.0.0.3", "198.51.100.1", true},
		{"malformed chain", "10.0.0.2:1234", "bogus, 198.51.100.1", "10.0.0.2", true},
		{"absent header", "10.0.0.2:1234", "", "10.0.0.2", true},
		{"too many hops", "10.0.0.2:1234", strings.Repeat("10.0.0.3,", 32) + "198.51.100.1", "10.0.0.2", true},
		{"IPv6", "[2001:db8::1]:1234", "", "2001:db8::1", false},
		{"mapped IPv4", "[::ffff:10.0.0.2]:1234", "::ffff:198.51.100.1", "198.51.100.1", true},
		{"IPv6 proxy", "[2001:db8:1::1]:1234", "2001:db8:2::1", "2001:db8:2::1", true},
		{"zone in header", "10.0.0.2:1234", "fe80::1%eth0", "10.0.0.2", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &api{}
			if test.trust {
				api.trustedProxies = trusted
			}
			request := httptest.NewRequest("POST", "/api/session", nil)
			request.RemoteAddr = test.peer
			request.Header.Set("X-Forwarded-For", test.forwarded)
			request.Header.Set("CF-Connecting-IP", "203.0.113.60")
			if actual := api.clientIP(request); actual.String() != test.expected {
				t.Fatalf("clientIP=%v, want=%s", actual, test.expected)
			}
		})
	}
}
