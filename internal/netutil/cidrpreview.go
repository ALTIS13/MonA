package netutil

import (
	"fmt"
	"net"
	"strings"
)

type CIDRPreview struct {
	Valid      bool     `json:"valid"`
	Error      string   `json:"error,omitempty"`
	Spec       string   `json:"spec,omitempty"`
	TotalHosts int      `json:"total_hosts"`
	First      string   `json:"first,omitempty"`
	Last       string   `json:"last,omitempty"`
	Samples    []string `json:"samples,omitempty"`
}

// PreviewSpec supports:
// - CIDR: "10.10.0.0/16"
// - Range: "10.10.1.10-10.10.1.200"
// - Multi: "10.10.1.10-10.10.1.200,10.10.2.10-10.10.2.200"
func PreviewSpec(spec string) CIDRPreview {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return CIDRPreview{Valid: false, Error: "empty"}
	}

	parts := splitSpec(spec)
	var total int
	var first, last net.IP
	samples := []string{}

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") {
			pr := previewCIDR(p)
			if !pr.Valid {
				return CIDRPreview{Valid: false, Error: pr.Error}
			}
			if pr.TotalHosts == 0 {
				continue
			}
			total += pr.TotalHosts
			if pr.First != "" {
				f := net.ParseIP(pr.First).To4()
				l := net.ParseIP(pr.Last).To4()
				if first == nil || bytesLE(f, first) {
					first = f
				}
				if last == nil || bytesLE(last, l) {
					last = l
				}
			}
			samples = appendSamples(samples, pr.Samples)
			continue
		}

		// Range
		rs, err := parseRange(p)
		if err != nil {
			return CIDRPreview{Valid: false, Error: err.Error()}
		}
		if rs.TotalHosts == 0 {
			continue
		}
		total += rs.TotalHosts
		if first == nil || bytesLE(rs.first, first) {
			first = rs.first
		}
		if last == nil || bytesLE(last, rs.last) {
			last = rs.last
		}
		samples = appendSamples(samples, rs.Samples)
	}

	out := CIDRPreview{
		Valid:      true,
		Spec:       spec,
		TotalHosts: total,
	}
	if first != nil {
		out.First = first.String()
	}
	if last != nil {
		out.Last = last.String()
	}
	out.Samples = shrinkSamples(samples)
	return out
}

func PreviewCIDR(cidr string) CIDRPreview { return PreviewSpec(cidr) }

func previewCIDR(cidr string) CIDRPreview {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return CIDRPreview{Valid: false, Error: err.Error()}
	}
	ip := n.IP.To4()
	if ip == nil {
		return CIDRPreview{Valid: false, Error: "only IPv4 is supported for discovery"}
	}

	network := ip.Mask(n.Mask)
	mask := net.IP(n.Mask).To4()
	broadcast := make(net.IP, len(network))
	for i := 0; i < 4; i++ {
		broadcast[i] = network[i] | ^mask[i]
	}

	start := make(net.IP, len(network))
	copy(start, network)
	inc4(start)

	end := make(net.IP, len(broadcast))
	copy(end, broadcast)
	dec4(end)

	if bytesLE(end, start) {
		return CIDRPreview{Valid: true, Spec: cidr, TotalHosts: 0}
	}

	total := distance4(start, end) + 1
	out := CIDRPreview{
		Valid:      true,
		Spec:       cidr,
		TotalHosts: total,
		First:      start.String(),
		Last:       end.String(),
	}

	// Sample first/last few
	s := []string{}
	cur := make(net.IP, len(start))
	copy(cur, start)
	for i := 0; i < 3 && !bytesLE(end, cur); i++ {
		s = append(s, cur.String())
		inc4(cur)
	}
	if total > 6 {
		s = append(s, "…")
	}
	last := make(net.IP, len(end))
	copy(last, end)
	for i := 0; i < 3 && !bytesLE(last, start); i++ {
		s = append(s, last.String())
		dec4(last)
	}
	out.Samples = s
	return out
}

type rangePreview struct {
	first      net.IP
	last       net.IP
	TotalHosts int
	Samples    []string
}

func parseRange(spec string) (rangePreview, error) {
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return rangePreview{}, fmt.Errorf("bad range %q, want A-B", spec)
	}
	a := net.ParseIP(strings.TrimSpace(parts[0])).To4()
	b := net.ParseIP(strings.TrimSpace(parts[1])).To4()
	if a == nil || b == nil {
		return rangePreview{}, fmt.Errorf("bad IPv4 range %q", spec)
	}
	if bytesLE(b, a) {
		return rangePreview{first: a, last: b, TotalHosts: 0}, nil
	}
	total := distance4(a, b) + 1
	s := []string{a.String()}
	if total > 2 {
		s = append(s, "…")
	}
	s = append(s, b.String())
	return rangePreview{first: a, last: b, TotalHosts: total, Samples: s}, nil
}

func splitSpec(spec string) []string {
	spec = strings.ReplaceAll(spec, "\n", ",")
	spec = strings.ReplaceAll(spec, "\r", ",")
	return strings.Split(spec, ",")
}

func appendSamples(dst, src []string) []string {
	for _, x := range src {
		if x == "" {
			continue
		}
		dst = append(dst, x)
	}
	return dst
}

func shrinkSamples(in []string) []string {
	if len(in) <= 9 {
		return in
	}
	// Keep head/tail.
	out := []string{}
	out = append(out, in[:3]...)
	out = append(out, "…")
	out = append(out, in[len(in)-3:]...)
	return out
}

func inc4(ip net.IP) {
	for i := 3; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
func dec4(ip net.IP) {
	for i := 3; i >= 0; i-- {
		ip[i]--
		if ip[i] != 255 {
			return
		}
	}
}
func bytesLE(a, b net.IP) bool {
	for i := 0; i < 4; i++ {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return false
}

func distance4(a, b net.IP) int {
	ai := int(a[0])<<24 | int(a[1])<<16 | int(a[2])<<8 | int(a[3])
	bi := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if bi < ai {
		return 0
	}
	return bi - ai
}

func FormatHosts(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1000000.0)
	}
}

