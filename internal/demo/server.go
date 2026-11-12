package demo

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// TrustedProxies is the network the demo's only peer, the cluster's ingress
// controller, connects from (notrf01's pod network). Only a request from
// there has its X-Forwarded-For read; the NetworkPolicy lets nothing else
// reach the pod. [MESHSAT-1845]
var TrustedProxies = []string{"10.2.0.0/16"}

// ClientIP is the visitor's address: the rightmost X-Forwarded-For entry
// that is not one of the trusted proxies, when the request came from a
// trusted proxy; else the TCP peer. A client cannot forge it: entries it
// sends sit left of what the ingress appends. IPv6 is cut to its /64, so a
// phone cannot walk its own prefix past the per-phone ceilings. (The
// pattern of meshsat-shop's realIP.)
func ClientIP(r *http.Request, trusted []*net.IPNet) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	ip := net.ParseIP(peer)
	if ip == nil || !inNets(ip, trusted) {
		return keyIP(ip, peer)
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		cand := net.ParseIP(h)
		if cand == nil {
			break
		}
		if inNets(cand, trusted) {
			continue
		}
		return keyIP(cand, h)
	}
	return keyIP(ip, peer)
}

func keyIP(ip net.IP, raw string) string {
	if ip == nil {
		return raw
	}
	if ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return ip.String()
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseNets parses CIDRs; a bad one is skipped.
func ParseNets(cidrs []string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// WithClientIP rewrites RemoteAddr to the visitor's address before the
// portal sees it: the portal keys its per-phone ceilings and its captive
// detection on RemoteAddr.
func WithClientIP(next http.Handler, trusted []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.RemoteAddr = net.JoinHostPort(ClientIP(r, trusted), "0")
		next.ServeHTTP(w, r2)
	})
}

// robots keeps both hosts out of search engines: a demo is not content.
const robots = "User-agent: *\nDisallow: /\n"

// common answers what both listeners answer the same way (the health check
// and robots.txt) and sets the headers every answer carries.
func common(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Robots-Tag", "noindex, nofollow")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		switch r.URL.Path {
		case "/healthz", "/health":
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(`{"status":"healthy","demo":true}`))
			return
		case "/robots.txt":
			h.Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(robots))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BridgeHandler is the bridge.meshsat.net listener: /api from the demo API,
// everything else the web app, all under the web app's CSP.
func BridgeHandler(api *API, spa *SPA) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", spa)
	return common(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", SPACSP)
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	}))
}

// PortalHandler is the app.safertogether.eu listener: the Community Post's
// own handler (it sets its own CSP and caps every body at 8 KB), with the
// visitor's address put where it looks for it.
func PortalHandler(portal http.Handler, trusted []*net.IPNet) http.Handler {
	return common(WithClientIP(portal, trusted))
}

// NewServer is an http.Server with every timeout set and small headers:
// a public listener must not let a slow client hold it.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
