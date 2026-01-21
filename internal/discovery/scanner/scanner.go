package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"asic-control/internal/defaultcreds"
	"asic-control/internal/modelnorm"
	"asic-control/internal/netutil"
)

type Config struct {
	Concurrency int
	DialTimeout time.Duration
	HTTPTimeout time.Duration
	Ports       []int

	TryDefaultCreds bool
}

type Result struct {
	IP       net.IP
	Online   bool
	Open     []int
	Vendor   string
	Firmware string
	Model    string

	IsASIC     bool
	Confidence int // 0..100

	Worker      string
	UptimeS     uint64
	HashrateTHS float64

	// Telemetry (best-effort; vendor specific)
	FansRPM []int     `json:"fans_rpm,omitempty"`
	TempsC  []float64 `json:"temps_c,omitempty"`
}

type Scanner struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Scanner {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 256
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 600 * time.Millisecond
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 1 * time.Second
	}
	if len(cfg.Ports) == 0 {
		// Common: web UIs + cgminer API + ssh.
		cfg.Ports = []int{80, 443, 4028, 22}
	}
	return &Scanner{
		cfg: cfg,
		http: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
	}
}

func (s *Scanner) ScanCIDR(ctx context.Context, cidr string, onProgress func(done, total int), onResult func(r Result)) error {
	return s.ScanSpec(ctx, cidr, onProgress, onResult)
}

func (s *Scanner) ScanSpec(ctx context.Context, spec string, onProgress func(done, total int), onResult func(r Result)) error {
	spec = strings.TrimSpace(spec)
	prev := netutil.PreviewSpec(spec)
	if !prev.Valid {
		return fmt.Errorf("bad spec: %s", prev.Error)
	}
	total := prev.TotalHosts
	if total == 0 {
		return nil
	}

	jobs := make(chan net.IP, s.cfg.Concurrency*2)
	var wg sync.WaitGroup

	done := 0
	var mu sync.Mutex
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	report := func() {
		if onProgress == nil {
			return
		}
		mu.Lock()
		d := done
		mu.Unlock()
		onProgress(d, total)
	}

	for i := 0; i < s.cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				r := s.probeIP(ctx, ip)
				if onResult != nil && (r.Online || len(r.Open) > 0) {
					onResult(r)
				}
				mu.Lock()
				done++
				mu.Unlock()
			}
		}()
	}

	go func() {
		for range tick.C {
			report()
		}
	}()

	for _, part := range splitSpec(spec) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, ipnet, err := net.ParseCIDR(part)
			if err != nil {
				continue
			}
			for _, ip := range enumerateIPv4(ipnet) {
				select {
				case <-ctx.Done():
					close(jobs)
					wg.Wait()
					return ctx.Err()
				case jobs <- ip:
				}
			}
			continue
		}

		a, b, ok := parseRangeIPs(part)
		if !ok {
			continue
		}
		for ip := a; ip <= b; ip++ {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return ctx.Err()
			case jobs <- u32ToIP(ip):
			}
		}
	}
	close(jobs)
	wg.Wait()
	report()
	return nil
}

func (s *Scanner) probeIP(ctx context.Context, ip net.IP) Result {
	r := Result{IP: ip}
	host := ip.String()

	open := make([]int, 0, len(s.cfg.Ports))
	for _, p := range s.cfg.Ports {
		ok := tcpOpen(ctx, host, p, s.cfg.DialTimeout)
		// Retry critical ports once (ASIC web/cgminer are sometimes slow to accept).
		if !ok && (p == 80 || p == 443 || p == 4028) {
			time.Sleep(25 * time.Millisecond)
			ok = tcpOpen(ctx, host, p, s.cfg.DialTimeout*2)
		}
		if ok {
			open = append(open, p)
		}
	}
	r.Open = open
	r.Online = len(open) > 0

	// Best-effort fingerprint via HTTP headers/body (no auth).
	if hasPort(open, 80) {
		s.fingerprintHTTP(ctx, &r, "http://"+host+"/")
	}
	if r.Vendor == "" && hasPort(open, 443) {
		s.fingerprintHTTP(ctx, &r, "https://"+host+"/")
	}

	// cgminer API (many miners expose it; provides uptime/hashrate/worker without creds).
	if hasPort(open, 4028) {
		s.fingerprintCGMiner(ctx, &r, host, 4028)
	}

	// Some devices require login to show details in HTTP UI.
	// Optional and limited to built-in default creds (no storage).
	if s.cfg.TryDefaultCreds && (hasPort(open, 80) || hasPort(open, 443)) {
		s.fingerprintHTTPWithDefaults(ctx, &r, host, hasPort(open, 443))
	}

	// Decide if this is an ASIC candidate.
	if r.Model != "" {
		n := modelnorm.Normalize(r.Model)
		if n.Model != "" {
			r.Model = n.Model
		}
		if (r.Vendor == "" || r.Vendor == "asic" || r.Vendor == "unknown") && n.Vendor != "unknown" {
			r.Vendor = n.Vendor
		}
	}
	r.Confidence = s.score(&r)
	r.IsASIC = r.Confidence >= 60

	return r
}

func (s *Scanner) fingerprintHTTPWithDefaults(ctx context.Context, r *Result, host string, https bool) {
	scheme := "http"
	if https {
		scheme = "https"
	}
	// Best-effort endpoints seen on some miner firmwares (varies a lot).
	paths := []string{
		"/cgi-bin/get_system_info.cgi",
		"/cgi-bin/stats.cgi",
		"/cgi-bin/summary.cgi",
	}

	for _, cred := range defaultcreds.Defaults() {
		for _, p := range paths {
			url := scheme + "://" + host + p
			req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
			req.SetBasicAuth(cred.Username, cred.Password)
			resp, err := s.http.Do(req)
			if err != nil {
				continue
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				_ = resp.Body.Close()
				continue
			}
			buf, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			_ = resp.Body.Close()
			if len(buf) == 0 {
				continue
			}
			body := strings.ToLower(string(buf))
			// lightweight vendor hints
			if r.Vendor == "" {
				switch {
				case strings.Contains(body, "antminer"):
					r.Vendor = "antminer"
				case strings.Contains(body, "whatsminer"):
					r.Vendor = "whatsminer"
				case strings.Contains(body, "iceriver"):
					r.Vendor = "iceriver"
				case strings.Contains(body, "avalon") || strings.Contains(body, "canaan"):
					r.Vendor = "avalonminer"
				case strings.Contains(body, "elphapex"):
					r.Vendor = "elphapex"
				}
			}
			// try parse common JSON fields if response looks like JSON
			var m map[string]any
			if json.Unmarshal(buf, &m) == nil {
				if r.Model == "" {
					if v, ok := m["model"].(string); ok {
						r.Model = v
					} else if v, ok := m["Model"].(string); ok {
						r.Model = v
					}
				}
				if r.Firmware == "" {
					if v, ok := m["firmware"].(string); ok {
						r.Firmware = v
					} else if v, ok := m["Firmware"].(string); ok {
						r.Firmware = v
					}
				}
			}
			// success: stop early
			return
		}
	}
}

func (s *Scanner) fingerprintHTTP(ctx context.Context, r *Result, url string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	// headers first
	server := strings.ToLower(resp.Header.Get("server"))
	if strings.Contains(server, "antminer") {
		r.Vendor = "antminer"
	}

	// small body sniff
	buf := make([]byte, 2048)
	n, _ := resp.Body.Read(buf)
	body := strings.ToLower(string(buf[:n]))

	switch {
	case strings.Contains(body, "antminer"):
		r.Vendor = "antminer"
	case strings.Contains(body, `meta name="firmware"`) && strings.Contains(body, "anthillos"):
		// Vnish/AnthillOS web UI (SPA). It's still an Antminer-class device.
		r.Vendor = "antminer"
		if r.Firmware == "" {
			r.Firmware = "AnthillOS"
		}
	case strings.Contains(body, "whatsminer"):
		r.Vendor = "whatsminer"
	case strings.Contains(body, "avalon") || strings.Contains(body, "canaan"):
		r.Vendor = "avalonminer"
	case strings.Contains(body, "iceriver"):
		r.Vendor = "iceriver"
	case strings.Contains(body, "elphapex"):
		r.Vendor = "elphapex"
	case strings.Contains(body, "cgminer") || strings.Contains(body, "bmminer"):
		// generic miner hints
		if r.Vendor == "" {
			r.Vendor = "asic"
		}
	}

	_ = url // future: parse model/firmware
}

func (s *Scanner) fingerprintCGMiner(ctx context.Context, r *Result, host string, port int) {
	// CGMiner API protocol: JSON text over TCP, response is JSON-ish with sections.
	// We keep it best-effort and tolerant.
	type summaryResp struct {
		SUMMARY []map[string]any `json:"SUMMARY"`
		STATUS  []map[string]any `json:"STATUS"`
	}
	type poolsResp struct {
		POOLS  []map[string]any `json:"POOLS"`
		STATUS []map[string]any `json:"STATUS"`
	}
	type devsResp struct {
		DEVS   []map[string]any `json:"DEVS"`
		STATUS []map[string]any `json:"STATUS"`
	}
	type statsResp struct {
		STATS  []map[string]any `json:"STATS"`
		STATUS []map[string]any `json:"STATUS"`
	}

	summaryRaw, ok := cgminerCommand(ctx, host, port, "summary")
	if ok {
		var sr summaryResp
		if json.Unmarshal([]byte(summaryRaw), &sr) == nil && len(sr.SUMMARY) > 0 {
			m := sr.SUMMARY[0]
			// Uptime
			if v, ok := m["Elapsed"]; ok {
				r.UptimeS = toU64(v)
			}
			// Hashrate: try multiple keys
			switch {
			case m["GHS 5s"] != nil:
				r.HashrateTHS = toF64(m["GHS 5s"]) / 1e3 // GHS -> THS
			case m["GHS av"] != nil:
				r.HashrateTHS = toF64(m["GHS av"]) / 1e3
			case m["MHS 5s"] != nil:
				r.HashrateTHS = toF64(m["MHS 5s"]) / 1e6 // MHS -> THS
			case m["MHS av"] != nil:
				r.HashrateTHS = toF64(m["MHS av"]) / 1e6
			}
		}
	}

	poolsRaw, ok := cgminerCommand(ctx, host, port, "pools")
	if ok {
		var pr poolsResp
		if json.Unmarshal([]byte(poolsRaw), &pr) == nil && len(pr.POOLS) > 0 {
			// take first enabled pool
			for _, p := range pr.POOLS {
				if user, ok := p["User"].(string); ok && strings.TrimSpace(user) != "" {
					r.Worker = user
					break
				}
			}
		}
	}

	// Try to get model/type (varies by firmware).
	devsRaw, ok := cgminerCommand(ctx, host, port, "devs")
	if ok {
		var dr devsResp
		if json.Unmarshal([]byte(devsRaw), &dr) == nil && len(dr.DEVS) > 0 {
			m := dr.DEVS[0]
			if r.Model == "" {
				if v, ok := m["Model"].(string); ok && strings.TrimSpace(v) != "" {
					r.Model = v
				} else if v, ok := m["Name"].(string); ok && strings.TrimSpace(v) != "" {
					r.Model = v
				} else if v, ok := m["Description"].(string); ok && strings.TrimSpace(v) != "" {
					r.Model = v
				}
			}
		}
	}

	statsRaw, ok := cgminerCommand(ctx, host, port, "stats")
	if ok {
		var sr statsResp
		if json.Unmarshal([]byte(statsRaw), &sr) == nil && len(sr.STATS) > 0 {
			fans := map[int]int{}
			temps := map[int]float64{}
			chip := ""
			for _, m := range sr.STATS {
				// firmware / version keys differ a lot
				if r.Firmware == "" {
					for _, k := range []string{"Firmware Version", "firmware", "version", "Miner Version", "BMMiner Version"} {
						if v, ok := m[k]; ok {
							if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
								r.Firmware = s
								break
							}
						}
					}
				}
				if r.Model == "" {
					for _, k := range []string{"Type", "Model", "Product", "Miner Type", "miner_type", "Device Model"} {
						if v, ok := m[k]; ok {
							if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
								r.Model = s
								break
							}
						}
					}
				}
				if chip == "" {
					for _, k := range []string{"Chip Type", "ChipType", "ASIC", "asic"} {
						if v, ok := m[k]; ok {
							if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
								chip = strings.ToUpper(strings.TrimSpace(s))
								break
							}
						}
					}
				}

				// Antminer/bmminer commonly exposes fan1..fan4 and temp1..temp3 in STATS
				for k, v := range m {
					kl := strings.ToLower(strings.TrimSpace(k))
					if strings.HasPrefix(kl, "fan") && len(kl) >= 4 {
						n, ok := parseSuffixInt(kl, "fan")
						if ok {
							rpm := int(toU64(v))
							if rpm > 0 {
								fans[n] = rpm
							} else if _, exists := fans[n]; !exists {
								// keep zeros so UI can show stopped fans
								fans[n] = 0
							}
						}
					}
					if strings.HasPrefix(kl, "temp") && len(kl) >= 5 {
						n, ok := parseSuffixInt(kl, "temp")
						if ok {
							t := toF64(v)
							if t != 0 {
								temps[n] = t
							}
						}
					}
				}
			}

			// chip-type fallback mapping (best-effort)
			if (strings.TrimSpace(strings.ToUpper(r.Model)) == "SOC" || strings.Contains(strings.ToUpper(r.Model), " SOC")) && chip != "" {
				switch {
				case strings.Contains(chip, "BM1370"):
					// S21 family. Rough differentiation by observed hashrate.
					if r.HashrateTHS >= 215 {
						r.Model = "S21 Pro"
					} else {
						r.Model = "S21"
					}
				case strings.Contains(chip, "BM1397"):
					r.Model = "S19"
				}
			}

			if len(fans) > 0 {
				r.FansRPM = denseInts(fans, 1, 8)
			}
			if len(temps) > 0 {
				r.TempsC = denseFloats(temps, 1, 8)
			}
		}
	}
}

func parseSuffixInt(s, prefix string) (int, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	prefix = strings.ToLower(prefix)
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(s, prefix)
	// only accept pure numeric suffix
	n := 0
	if rest == "" {
		return 0, false
	}
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

func denseInts(m map[int]int, from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		v, ok := m[i]
		if !ok {
			continue
		}
		out = append(out, v)
	}
	return out
}

func denseFloats(m map[int]float64, from, to int) []float64 {
	out := make([]float64, 0, to-from+1)
	for i := from; i <= to; i++ {
		v, ok := m[i]
		if !ok {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (s *Scanner) score(r *Result) int {
	score := 0
	if r.Vendor != "" && r.Vendor != "asic" {
		score += 40
	} else if r.Vendor == "asic" {
		score += 15
	}
	// If we strongly identified Bitmain/Antminer via HTTP, allow ASIC classification even without 4028 open.
	if r.Vendor == "antminer" && (hasPort(r.Open, 80) || hasPort(r.Open, 443)) {
		score += 15
	}
	if hasPort(r.Open, 4028) {
		score += 35
	}
	if hasPort(r.Open, 80) || hasPort(r.Open, 443) {
		score += 10
	}
	if r.Worker != "" {
		score += 10
	}
	if r.HashrateTHS > 0 {
		score += 5
	}
	if score > 100 {
		return 100
	}
	return score
}

func cgminerCommand(ctx context.Context, host string, port int, cmd string) (string, bool) {
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return "", false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(900 * time.Millisecond))

	req := fmt.Sprintf(`{"command":"%s"}`, cmd)
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", false
	}
	b, _ := io.ReadAll(conn)
	if len(b) == 0 {
		return "", false
	}
	// Some cgminer responses use NUL separators; replace with commas to improve JSON parse success.
	s := strings.ReplaceAll(string(b), "\x00", "")
	s = strings.TrimSpace(s)
	return s, true
}

func toU64(v any) uint64 {
	switch x := v.(type) {
	case float64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case string:
		var out uint64
		_, _ = fmt.Sscanf(strings.TrimSpace(x), "%d", &out)
		return out
	default:
		return 0
	}
}

func toF64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		var out float64
		_, _ = fmt.Sscanf(strings.TrimSpace(x), "%f", &out)
		return out
	default:
		return 0
	}
}

func splitSpec(spec string) []string {
	spec = strings.ReplaceAll(spec, "\n", ",")
	spec = strings.ReplaceAll(spec, "\r", ",")
	return strings.Split(spec, ",")
}

func parseRangeIPs(spec string) (uint32, uint32, bool) {
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	a := net.ParseIP(strings.TrimSpace(parts[0])).To4()
	b := net.ParseIP(strings.TrimSpace(parts[1])).To4()
	if a == nil || b == nil {
		return 0, 0, false
	}
	au := ipToU32(a)
	bu := ipToU32(b)
	if bu < au {
		return 0, 0, false
	}
	return au, bu, true
}

func ipToU32(ip net.IP) uint32 {
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}
func u32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
// no helper needed

func tcpOpen(ctx context.Context, host string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hasPort(ports []int, p int) bool {
	for _, x := range ports {
		if x == p {
			return true
		}
	}
	return false
}

func enumerateIPv4(n *net.IPNet) []net.IP {
	// For v0: IPv4 only (ASIC farms are typically IPv4). IPv6 later.
	ip := n.IP.To4()
	if ip == nil {
		return nil
	}
	mask := net.IP(n.Mask).To4()
	if mask == nil {
		return nil
	}

	network := ip.Mask(n.Mask)
	broadcast := make(net.IP, len(network))
	for i := 0; i < 4; i++ {
		broadcast[i] = network[i] | ^mask[i]
	}

	// start = network+1, end = broadcast-1
	start := make(net.IP, len(network))
	copy(start, network)
	inc4(start)

	end := make(net.IP, len(broadcast))
	copy(end, broadcast)
	dec4(end)

	if bytesLE(end, start) {
		return nil
	}

	var out []net.IP
	cur := make(net.IP, len(start))
	copy(cur, start)
	for {
		cp := make(net.IP, len(cur))
		copy(cp, cur)
		out = append(out, cp)
		if equal4(cur, end) {
			break
		}
		inc4(cur)
	}
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
func equal4(a, b net.IP) bool {
	return a[0] == b[0] && a[1] == b[1] && a[2] == b[2] && a[3] == b[3]
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

