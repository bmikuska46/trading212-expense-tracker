package httpserver

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (a *api) trustedProxy(address netip.Addr) bool {
	for _, prefix := range a.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (a *api) clientIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap().WithZone("")
	if !a.trustedProxy(peer) {
		return peer
	}
	header := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	hops := strings.Split(header, ",")
	if len(hops) > 32 {
		return peer
	}
	addresses := make([]netip.Addr, len(hops))
	for i, hop := range hops {
		address, err := netip.ParseAddr(strings.TrimSpace(hop))
		if err != nil || address.Zone() != "" {
			return peer
		}
		addresses[i] = address.Unmap()
	}
	address := peer
	for i := len(addresses) - 1; i >= 0 && a.trustedProxy(address); i-- {
		address = addresses[i]
	}
	return address
}
