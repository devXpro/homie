package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/devXpro/mcp-gatekeeper/httpsec"
)

// hostGuard lets through only requests whose Host is the hub's public host (any port) or this hub's own loopback
// address, and answers anything else with 421. It is the DNS-rebinding guard of the public listener: the gateway turns
// off the go-sdk's own guard, because cloudflared's requests reach 127.0.0.1 with the public Host. host is read on
// every request, so a quick tunnel's new host is accepted at once and its old one refused; "" means no public host.
func hostGuard(host func() string, port int, next http.Handler) http.Handler {
	p := strconv.Itoa(port)
	local := map[string]bool{"127.0.0.1:" + p: true, "localhost:" + p: true, "[::1]:" + p: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.ToLower(r.Host)
		if h := strings.ToLower(host()); local[got] || (h != "" && hostname(got) == h) {
			next.ServeHTTP(w, r)
			return
		}
		httpsec.WriteError(w, http.StatusMisdirectedRequest, "misdirected", "this hub does not serve that host")
	})
}

// hostname drops the port and a trailing dot from a Host header value.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}
