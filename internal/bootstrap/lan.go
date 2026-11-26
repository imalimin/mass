package bootstrap

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// EnableLAN derives the HTTP origin and listener from an address on this host.
func (c *Config) EnableLAN(address string, port int) error {
	if port < 1 || port > 65535 {
		return errors.New("--port must be between 1 and 65535")
	}
	var requested netip.Addr
	if address != "" {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsPrivate() {
			return errors.New("--lan-ip must be a private IPv4 or IPv6 address")
		}
		requested = ip.Unmap()
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("list network interfaces: %w", err)
	}
	var privateIPs []netip.Addr
	for _, device := range interfaces {
		if device.Flags&net.FlagUp == 0 || device.Flags&net.FlagLoopback != 0 ||
			(!requested.IsValid() && device.Flags&net.FlagPointToPoint != 0) {
			continue
		}
		addresses, err := device.Addrs()
		if err != nil {
			return fmt.Errorf("list addresses for %s: %w", device.Name, err)
		}
		for _, addr := range addresses {
			prefix, err := netip.ParsePrefix(addr.String())
			if err == nil && prefix.Addr().Unmap().IsPrivate() {
				privateIPs = append(privateIPs, prefix.Addr().Unmap())
			}
		}
	}
	return c.configureLAN(privateIPs, requested, uint16(port))
}

func (c *Config) configureLAN(privateIPs []netip.Addr, requested netip.Addr, port uint16) error {
	var selected netip.Addr
	for _, ip := range privateIPs {
		if requested.IsValid() {
			if ip == requested {
				selected = ip
				break
			}
			continue
		}
		if !selected.IsValid() || ip.Is4() {
			selected = ip
		}
		if selected.Is4() {
			break
		}
	}
	if !selected.IsValid() {
		if requested.IsValid() {
			return errors.New("--lan-ip is not assigned to an active network interface on this host")
		}
		return errors.New("no active private LAN address found; use --lan-ip to select a local address")
	}
	c.HTTP.Listen = netip.AddrPortFrom(selected, port).String()
	c.HTTP.PublicURL = httpOrigin(c.HTTP.Listen)
	c.Auth.CookieSecure = false
	c.lanLoopback = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port).String()
	return nil
}

func httpOrigin(address string) string {
	return "http://" + strings.TrimSuffix(address, ":80")
}

func localAPIBaseURL(c Config) string {
	if c.lanLoopback != "" {
		return httpOrigin(c.lanLoopback)
	}
	addr := strings.TrimSpace(c.HTTP.Listen)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return httpOrigin(addr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return httpOrigin(net.JoinHostPort(host, port))
}

func peerBaseURL(c Config) string {
	if addr := strings.TrimSpace(c.Runtime.PeerAddr); addr != "" {
		return strings.TrimRight(addr, "/")
	}
	if env := strings.TrimSpace(os.Getenv("MASS_PEER_ADDR")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return localAPIBaseURL(c)
}
