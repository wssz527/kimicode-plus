package plugins

import (
	"context"
	"fmt"
	"net"
	"time"
)

// A plugin's `net:<host>` grant is a statement about a host, but the connection
// needs an address. Resolving the name twice — once to authorize, once to dial —
// is what makes DNS rebinding work: the name can resolve to a public address
// when the grant is checked and to loopback when the socket is opened.
//
// The guard below resolves exactly once per request, validates every returned
// address, and then dials the literal IP. The dialer never asks the resolver
// again, so the address the check saw is the address that gets connected to.

// netScope reports whether an address is acceptable as an outbound target. A
// loopback, link-local, private or otherwise non-global address is refused by
// default: a plugin granted `net:intranet.example` must not be able to reach the
// user's router, a cloud metadata endpoint or another local service just because
// a DNS answer pointed there.
func isBlockedAddress(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// IsPrivate covers 10/8, 172.16/12, 192.168/16 and fc00::/7.
	if ip.IsPrivate() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 100.64/10 carrier-grade NAT, 192.0.0/24 IETF protocol assignments,
		// 198.18/15 benchmarking, 240/4 reserved and 255.255.255.255 broadcast.
		switch {
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
			return true
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return true
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19):
			return true
		case ip4[0] >= 240:
			return true
		}
		return false
	}
	// IPv6 unique-local (fc00::/7) is covered by IsPrivate; the remaining
	// special-purpose ranges are not routable from a normal host.
	if len(ip) == net.IPv6len {
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
			return true // 2001:db8::/32 documentation
		}
	}
	return false
}

// A user who wrote `net:127.0.0.1` explicitly approved that address, so it is
// exempt from the private-address rule; a name that merely resolves there is not.
// resolveApprovedAddr resolves an authorized host and returns the single address
// the dialer must use. Every answer is checked: a name that resolves to mixed
// public and private addresses is refused rather than "first one wins", because
// otherwise the answer order decides whether the request is safe.
func resolveApprovedAddr(ctx context.Context, lookupHost func(context.Context, string) ([]net.IPAddr, error), host string) (net.IP, error) {
	if ip, ok := canonicalIP(host); ok {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			return nil, apiError(403, "PLUGIN_NET_HOST_DENIED", "插件网络目标地址无效")
		}
		// The user named this address in the grant: approving it in `net:` is the
		// explicit decision that lets a local or private target through.
		return parsed, nil
	}
	addresses, err := lookupHost(ctx, host)
	if err != nil {
		return nil, apiError(502, "PLUGIN_NET_DNS_FAILED", "无法解析 "+host)
	}
	if len(addresses) == 0 {
		return nil, apiError(502, "PLUGIN_NET_DNS_FAILED", "无法解析 "+host)
	}
	var approved net.IP
	for _, address := range addresses {
		if isBlockedAddress(address.IP) {
			return nil, apiError(403, "PLUGIN_NET_HOST_DENIED", fmt.Sprintf("%s 解析到本机或内网地址 %s，已拒绝", host, address.IP.String()))
		}
		if approved == nil {
			approved = address.IP
		}
	}
	return approved, nil
}

// guardedDialer returns a dial function bound to one approved address. The
// transport is created per request, so a pooled connection can never be reused
// for a different host than the one whose resolution was just approved.
func guardedDialer(addr net.IP, port string) func(ctx context.Context, network, _ string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	target := net.JoinHostPort(addr.String(), port)
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, target)
	}
}
