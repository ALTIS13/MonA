//go:build mikrotik
// +build mikrotik

package mikrotik

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/go-routeros/routeros"
)

type ARPEntry struct {
	IP        net.IP
	MAC       string
	Interface string
	Dynamic   bool
}

type DHCPLease struct {
	IP        net.IP
	MAC       string
	Hostname  string
	Status    string
	Server    string
	Interface string
	Dynamic   bool
}

type Client interface {
	ListARP(ctx context.Context) ([]ARPEntry, error)
	ListDHCP(ctx context.Context) ([]DHCPLease, error)
	Close() error
}

type RouterOS struct {
	c *routeros.Client
}

type RouterOSConfig struct {
	Address  string
	Username string
	Password string
	Timeout  time.Duration
}

func Dial(cfg RouterOSConfig) (*RouterOS, error) {
	// go-routeros doesn't accept context; use timeout via net.Dialer.
	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.Dial("tcp", cfg.Address)
	if err != nil {
		return nil, err
	}
	c, err := routeros.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := c.Login(cfg.Username, cfg.Password); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &RouterOS{c: c}, nil
}

func (r *RouterOS) Close() error {
	if r.c == nil {
		return nil
	}
	return r.c.Close()
}

func (r *RouterOS) ListARP(ctx context.Context) ([]ARPEntry, error) {
	// RouterOS API: /ip/arp/print
	// Note: routeros client isn't context-aware; keep this call lightweight.
	rep, err := r.c.Run("/ip/arp/print")
	if err != nil {
		return nil, err
	}
	out := make([]ARPEntry, 0, len(rep.Re))
	for _, re := range rep.Re {
		ip := net.ParseIP(re.Map["address"])
		if ip == nil {
			continue
		}
		out = append(out, ARPEntry{
			IP:        ip,
			MAC:       normalizeMAC(re.Map["mac-address"]),
			Interface: re.Map["interface"],
			Dynamic:   re.Map["dynamic"] == "true",
		})
	}
	return out, nil
}

func (r *RouterOS) ListDHCP(ctx context.Context) ([]DHCPLease, error) {
	rep, err := r.c.Run("/ip/dhcp-server/lease/print")
	if err != nil {
		return nil, err
	}
	out := make([]DHCPLease, 0, len(rep.Re))
	for _, re := range rep.Re {
		ip := net.ParseIP(re.Map["address"])
		if ip == nil {
			continue
		}
		out = append(out, DHCPLease{
			IP:        ip,
			MAC:       normalizeMAC(re.Map["mac-address"]),
			Hostname:  re.Map["host-name"],
			Status:    re.Map["status"],
			Server:    re.Map["server"],
			Interface: re.Map["bridge"], // on many setups, lease reports bridge
			Dynamic:   re.Map["dynamic"] == "true",
		})
	}
	return out, nil
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", ":")
	if len(s) == 12 && !strings.Contains(s, ":") {
		// 001122aabbcc -> 00:11:22:aa:bb:cc
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(s[i : i+2])
		}
		return b.String()
	}
	return s
}

func MustCIDRs(csv string) ([]*net.IPNet, error) {
	if strings.TrimSpace(csv) == "" {
		return nil, nil
	}
	parts := strings.Split(csv, ",")
	out := make([]*net.IPNet, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return nil, fmt.Errorf("parse cidr %q: %w", p, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func ContainsAny(nets []*net.IPNet, ip net.IP) bool {
	if len(nets) == 0 {
		return true
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

