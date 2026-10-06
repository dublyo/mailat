package middleware

import (
	"net"
	"strings"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/gogf/gf/v2/net/ghttp"
)

var loopbackNets, _ = config.ParseProxyNets("127.0.0.1/32,::1/128")

// trustedProxyNets prefers the networks parsed by config.Load, falls back to
// the raw setting for directly built configs, and trusts only loopback when
// nothing is configured.
func trustedProxyNets() []*net.IPNet {
	cfg := config.Cfg
	if cfg == nil {
		return loopbackNets
	}
	if cfg.TrustedProxyNets != nil {
		return cfg.TrustedProxyNets
	}
	if nets, err := config.ParseProxyNets(cfg.TrustedProxyCIDRs); err == nil && len(nets) > 0 {
		return nets
	}
	return loopbackNets
}

// ClientIP is the only supported way to read a client's address. Forwarding
// headers are honored only when the TCP peer is a trusted proxy, so consent
// evidence, audit logs and rate-limit subjects cannot be spoofed by a visitor.
func ClientIP(r *ghttp.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	nets := trustedProxyNets()
	trusted := func(raw string) bool {
		ip := net.ParseIP(raw)
		if ip == nil {
			return false
		}
		for _, network := range nets {
			if network.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(host) {
		return host
	}
	// Walk from the immediate peer toward the client and stop at the first
	// untrusted hop. A visitor cannot spoof the result with a prepended address.
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(chain[i])
		if net.ParseIP(candidate) == nil {
			continue
		}
		host = candidate
		if !trusted(host) {
			break
		}
	}
	return host
}
