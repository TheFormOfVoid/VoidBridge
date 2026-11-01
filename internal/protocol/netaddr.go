package protocol

import "net"

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// PrivateIP reports whether ip is on a home network or Tailscale rather than
// the internet: loopback, private ranges (10/8, 172.16/12, 192.168/16, IPv6
// ULA), link-local, or carrier-grade NAT (100.64/10, used by Tailscale).
// Plain HTTP is only allowed to and from these.
func PrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}
