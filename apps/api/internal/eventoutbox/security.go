// Package eventoutbox persists webhook events with their source transaction and
// delivers one stable, signed payload through a retryable database outbox.
package eventoutbox

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func Sign(body []byte, secret string, at time.Time) string {
	stamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "."))
	mac.Write(body)
	return "t=" + stamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func Verify(body []byte, signature, secret string, tolerance time.Duration) bool {
	if secret == "" {
		return false
	}
	var stamp, digest string
	seen := map[string]bool{}
	for _, part := range strings.Split(signature, ",") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 || seen[pair[0]] {
			return false
		}
		seen[pair[0]] = true
		switch pair[0] {
		case "t":
			if stamp != "" {
				return false
			}
			stamp = pair[1]
		case "v1":
			if digest != "" {
				return false
			}
			digest = pair[1]
		default:
			return false
		}
	}
	timestamp, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || timestamp <= 0 || strconv.FormatInt(timestamp, 10) != stamp || len(digest) != 64 {
		return false
	}
	if tolerance <= 0 {
		tolerance = 5 * time.Minute
	}
	diff := time.Since(time.Unix(timestamp, 0))
	if diff > tolerance || diff < -tolerance {
		return false
	}
	expected := Sign(body, secret, time.Unix(timestamp, 0))
	return subtle.ConstantTimeCompare([]byte(signatureDigest(expected)), []byte(digest)) == 1
}
func signatureDigest(signature string) string { return strings.SplitN(signature, ",v1=", 2)[1] }

// ValidateDestination rejects unsafe URL forms without making a network request.
// Every connection repeats address checks after DNS resolution, preventing a
// public name from rebinding to a private destination between attempts.
func ValidateDestination(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 2048 {
		return fmt.Errorf("webhook URL must be a public HTTPS URL without credentials or a fragment")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid webhook port")
		}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.Contains(host, "%") || !strings.Contains(host, ".") && !strings.Contains(host, ":") {
		return fmt.Errorf("webhook destination must be public")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) {
		return fmt.Errorf("webhook destination must be public")
	}
	return nil
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("fec0::/10"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func SafeClient() *http.Client {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid webhook destination")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("webhook DNS lookup failed")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, fmt.Errorf("webhook DNS resolved to a non-public address")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &http.Client{Timeout: 20 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("webhook redirects are not allowed") }}
}
