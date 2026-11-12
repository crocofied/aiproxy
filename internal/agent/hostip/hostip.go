// Package hostip finds the IP address under which this host is reachable.
package hostip

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Detect returns the local IP address the operating system would use to
// reach the controller. On a host with several interfaces (LAN, Docker
// bridges, VPN) that is the one in the same network as the controller,
// which is exactly the address the proxy can reach us on.
//
// The trick: "connecting" a UDP socket sends no packet at all. It only
// makes the kernel pick a route, and with it a source address, which we
// then read back.
//
// This yields the host's address only if the agent shares the host's
// network namespace (network_mode: host). In a bridge network it returns
// the container's own address; set the host IP explicitly in that case.
func Detect(ctx context.Context, controller *url.URL) (string, error) {
	port := controller.Port()
	if port == "" {
		port = "80"
		if controller.Scheme == "https" {
			port = "443"
		}
	}
	target := net.JoinHostPort(controller.Hostname(), port)

	// The timeout only matters for the DNS lookup of the host name.
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", target)
	if err != nil {
		return "", fmt.Errorf("detect host IP: %w", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("detect host IP: unexpected address type %T", conn.LocalAddr())
	}
	// Unmap turns an IPv4-mapped IPv6 address (::ffff:192.0.2.1) into
	// plain IPv4.
	return local.AddrPort().Addr().Unmap().String(), nil
}
