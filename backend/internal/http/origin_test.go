package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestSignInOriginTrustsCloudflareOnlyThroughProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
	for _, test := range []struct {
		name, peer, forwarded, connecting string
		expected                          origin
	}{
		{"tunnel", "10.0.0.2:1234", "198.51.100.1, 10.0.0.3", "198.51.100.1",
			origin{ip: "198.51.100.1", country: "SK", region: "Bratislava Region", city: "Bratislava"}},
		{"direct client", "192.0.2.1:1234", "198.51.100.1", "198.51.100.1", origin{ip: "192.0.2.1"}},
		{"different Cloudflare IP", "10.0.0.2:1234", "198.51.100.1", "203.0.113.5", origin{ip: "198.51.100.1"}},
		{"no Cloudflare", "10.0.0.2:1234", "198.51.100.1", "", origin{ip: "198.51.100.1"}},
		{"proxy without forwarding", "10.0.0.2:1234", "", "10.0.0.2", origin{ip: "10.0.0.2", country: "SK", region: "Bratislava Region", city: "Bratislava"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &api{trustedProxies: trusted}
			request := httptest.NewRequest("POST", "/api/session", nil)
			request.RemoteAddr = test.peer
			request.Header.Set("X-Forwarded-For", test.forwarded)
			request.Header.Set("CF-Connecting-IP", test.connecting)
			request.Header.Set("CF-IPCountry", "sk")
			request.Header.Set("CF-Region", "Bratislava Region")
			request.Header.Set("CF-IPCity", "Bratislava")
			if actual := api.signInOrigin(request); actual != test.expected {
				t.Fatalf("origin=%+v, want=%+v", actual, test.expected)
			}
		})
	}
}

func TestSignInOriginCleansHeaders(t *testing.T) {
	api := &api{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}}
	request := httptest.NewRequest("POST", "/api/session", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "2001:db8::1")
	request.Header.Set("CF-Connecting-IP", "2001:db8::1")
	request.Header.Set("CF-IPCountry", "XX")
	request.Header.Set("CF-IPCity", "Ko\x00šice")
	request.Header.Set("CF-Region", "\xff")
	request.Header.Set("User-Agent", strings.Repeat("é", 200))
	actual := api.signInOrigin(request)
	if actual.ip != "2001:db8::1" || actual.country != "" || actual.city != "Košice" || actual.region != "" {
		t.Fatalf("origin=%+v", actual)
	}
	if len(actual.userAgent) != maxUserAgentBytes || actual.userAgent != strings.Repeat("é", maxUserAgentBytes/2) {
		t.Fatalf("user agent has %d bytes", len(actual.userAgent))
	}
	request.Header.Set("CF-IPCountry", "T1")
	if country := api.signInOrigin(request).country; country != "T1" {
		t.Fatalf("Tor country=%q", country)
	}
}

func TestListSessionsShowsWhereUserSignedIn(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Now: func() time.Time { return now },
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.1/32")}})
	first := app.request("POST", "/api/setup", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil,
		"X-Forwarded-For", "198.51.100.7", "CF-Connecting-IP", "198.51.100.7", "CF-IPCountry", "SK",
		"CF-IPCity", "Bratislava", "CF-Region", "Bratislava Region", "User-Agent", "Firefox")
	if first.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", first.Code, first.Body)
	}
	now = now.Add(time.Hour)
	second := app.request("POST", "/api/session", map[string]string{"email": ownerEmail, "password": ownerPassword}, nil,
		"X-Forwarded-For", "203.0.113.9", "User-Agent", "Safari")
	if second.Code != http.StatusOK {
		t.Fatalf("sign in: %d %s", second.Code, second.Body)
	}
	response := app.request("GET", "/api/sessions", nil, sessionCookie(t, first))
	if response.Code != http.StatusOK {
		t.Fatalf("sessions: %d %s", response.Code, response.Body)
	}
	sessions := decode[struct{ Sessions []sessionEntry }](t, response).Sessions
	expected := []sessionEntry{
		{CreatedAt: "2026-10-04T10:00:00Z", IP: "203.0.113.9", UserAgent: "Safari"},
		{CreatedAt: "2026-10-04T09:00:00Z", IP: "198.51.100.7", Country: "SK", Region: "Bratislava Region", City: "Bratislava",
			UserAgent: "Firefox", Current: true},
	}
	if len(sessions) != len(expected) || sessions[0] != expected[0] || sessions[1] != expected[1] {
		t.Fatalf("sessions=%+v", sessions)
	}
	if response := app.request("GET", "/api/sessions", nil, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", response.Code)
	}
}
