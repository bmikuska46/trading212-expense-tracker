package httpserver

import (
	"bytes"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"budget-tracking/backend/internal/auth"
	"budget-tracking/backend/internal/database/dbgen"
)

const (
	maxPlaceBytes     = 100
	maxUserAgentBytes = 300
)

// origin describes where a sign-in came from.
type origin struct {
	ip, country, region, city, userAgent string
}

// signInOrigin reads the client IP, Cloudflare's visitor location and the
// browser of a sign-in. Cloudflare's headers are trusted only when the request
// came through the trusted proxies and Cloudflare saw the same client IP the
// X-Forwarded-For chain resolves to, so a direct client cannot fake a place.
// Cloudflare always sends CF-IPCountry; the region and city need the "Add
// visitor location headers" managed transform.
func (a *api) signInOrigin(r *http.Request) origin {
	result := origin{userAgent: cleanHeader(r.Header.Get("User-Agent"), maxUserAgentBytes)}
	client := a.clientIP(r)
	if !client.IsValid() {
		return result
	}
	result.ip = client.String()
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !a.trustedProxy(peer.Unmap().WithZone("")) {
		return result
	}
	connecting, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP")))
	if err != nil || connecting.Unmap() != client {
		return result
	}
	// XX is an unknown country and T1 the Tor network.
	if country := strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry"))); countryCode(country) && country != "XX" {
		result.country = country
	}
	result.region = cleanHeader(r.Header.Get("CF-Region"), maxPlaceBytes)
	result.city = cleanHeader(r.Header.Get("CF-IPCity"), maxPlaceBytes)
	return result
}

func countryCode(value string) bool {
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' &&
		(value[1] >= 'A' && value[1] <= 'Z' || value[1] >= '0' && value[1] <= '9')
}

// cleanHeader keeps printable UTF-8 text, cut to a whole rune within limit bytes.
func cleanHeader(value string, limit int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, value))
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

type sessionEntry struct {
	CreatedAt string `json:"createdAt"`
	IP        string `json:"ip"`
	Country   string `json:"country"`
	Region    string `json:"region"`
	City      string `json:"city"`
	UserAgent string `json:"userAgent"`
	// Current is the session that made this request.
	Current bool `json:"current"`
}

// listSessions shows the signed-in user where their active sessions were
// signed in from, newest first.
func (a *api) listSessions(w http.ResponseWriter, r *http.Request, current *session) {
	rows, err := a.main.Queries.ListUserSessions(r.Context(), dbgen.ListUserSessionsParams{UserID: current.userID, Now: auth.FormatTime(a.now())})
	if err != nil {
		internalError(w, "list sessions")
		return
	}
	sessions := make([]sessionEntry, len(rows))
	for i, row := range rows {
		sessions[i] = sessionEntry{CreatedAt: row.CreatedAt, IP: row.Ip, Country: row.Country, Region: row.Region, City: row.City,
			UserAgent: row.UserAgent, Current: bytes.Equal(row.TokenHash, current.tokenHash)}
	}
	writeJSON(w, http.StatusOK, map[string][]sessionEntry{"sessions": sessions})
}
