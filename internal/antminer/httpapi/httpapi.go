package httpapi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type ProbeResult struct {
	OK        bool              `json:"ok"`
	Scheme    string            `json:"scheme,omitempty"`
	UsedCred  string            `json:"used_cred,omitempty"`
	Error     string            `json:"error,omitempty"`
	Responses map[string]any    `json:"responses,omitempty"` // endpoint -> parsed json or string
	Raw       map[string]string `json:"raw,omitempty"`       // endpoint -> raw (truncated)
}

type Cred struct {
	Name     string
	Username string
	Password string
}

// ProbeAntminerSchemes probes Antminer HTTP CGI endpoints using the given schemes (http/https)
// and tries all credentials. It returns the "best" successful result (most parsable facts),
// not just the first OK response.
//
// Important: ASIC web servers are often fragile with keep-alive. We force Connection: close.
func ProbeAntminerSchemes(ctx context.Context, host string, creds []Cred, schemes []string) ProbeResult {
	if len(schemes) == 0 {
		schemes = []string{"http"}
	}
	endpoints := []string{
		"/cgi-bin/get_system_info.cgi",
		"/cgi-bin/summary.cgi",
		"/cgi-bin/stats.cgi",
	}

	// Disable keep-alives to avoid:
	// "Unsolicited response received on idle HTTP channel ..."
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 1400 * time.Millisecond, KeepAlive: -1}).DialContext,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: 0,
		IdleConnTimeout:     0,
		TLSHandshakeTimeout: 900 * time.Millisecond,
		TLSClientConfig: &tls.Config{
			// Miners often use self-signed certs and odd TLS stacks.
			InsecureSkipVerify: true, //nolint:gosec
			MinVersion:         tls.VersionTLS10,
		},
	}
	client := &http.Client{Timeout: 3500 * time.Millisecond, Transport: tr}

	sanitize := func(b []byte) []byte {
		// Some firmwares prepend junk; try to find JSON start.
		s := string(b)
		if i := strings.Index(s, "{"); i >= 0 {
			return []byte(strings.TrimSpace(s[i:]))
		}
		if i := strings.Index(s, "["); i >= 0 {
			return []byte(strings.TrimSpace(s[i:]))
		}
		return b
	}

	perReqTimeout := func(ctx context.Context, scheme string) time.Duration {
		base := 2400 * time.Millisecond
		if scheme == "https" {
			base = 3200 * time.Millisecond
		}
		if dl, ok := ctx.Deadline(); ok {
			rem := time.Until(dl)
			if rem > 0 && rem < base+400*time.Millisecond {
				// keep some room for response parsing
				adj := rem / 2
				if adj < 900*time.Millisecond {
					adj = 900 * time.Millisecond
				}
				return adj
			}
		}
		return base
	}

	tryScheme := func(scheme string, cred Cred) ProbeResult {
		out := ProbeResult{
			Responses: map[string]any{},
			Raw:       map[string]string{},
			Scheme:    scheme,
			UsedCred:  cred.Name,
		}
		okAny := false
		lastErr := ""

		for _, p := range endpoints {
			reqCtx, cancel := context.WithTimeout(ctx, perReqTimeout(ctx, scheme))
			url := scheme + "://" + host + p
			req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
			req.Close = true
			req.Header.Set("Connection", "close")
			req.Header.Set("User-Agent", "MonA/asic-control")
			req.Header.Set("Accept", "application/json,text/plain;q=0.9,*/*;q=0.8")
			if cred.Username != "" || cred.Password != "" {
				req.SetBasicAuth(cred.Username, cred.Password)
			}

			resp, err := client.Do(req)
			if err != nil {
				lastErr = err.Error()
				cancel()
				continue
			}

			// IMPORTANT: don't cancel the request context before we read the body,
			// otherwise net/http may abort body read and we get "OK but empty/partial".
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
			_ = resp.Body.Close()
			cancel()

			if resp.StatusCode == 401 {
				// Try Digest auth fallback (common on lighttpd) if Basic didn't work.
				ch, ok := parseDigestChallenge(resp.Header.Get("WWW-Authenticate"))
				if ok && cred.Username != "" {
					// retry once with Digest
					ctx2, cancel2 := context.WithTimeout(ctx, perReqTimeout(ctx, scheme))
					req2, _ := http.NewRequestWithContext(ctx2, "GET", url, nil)
					req2.Close = true
					req2.Header.Set("Connection", "close")
					req2.Header.Set("User-Agent", "MonA/asic-control")
					req2.Header.Set("Accept", "application/json,text/plain;q=0.9,*/*;q=0.8")
					req2.Header.Set("Authorization", buildDigestAuth(cred.Username, cred.Password, "GET", p, ch))
					resp2, err := client.Do(req2)
					if err == nil {
						b2, _ := io.ReadAll(io.LimitReader(resp2.Body, 256*1024))
						_ = resp2.Body.Close()
						cancel2()
						body2 := strings.TrimSpace(string(b2))
						if resp2.StatusCode >= 200 && resp2.StatusCode <= 299 && body2 != "" {
							low2 := strings.ToLower(body2)
							if !(strings.HasPrefix(low2, "<!doctype html") || strings.HasPrefix(low2, "<html")) {
								var m2 any
								sb2 := sanitize(b2)
								if json.Unmarshal(sb2, &m2) == nil {
									okAny = true
									out.Responses[p] = m2
									if len(body2) > 4096 {
										body2 = body2[:4096] + "…"
									}
									out.Raw[p] = body2
									continue
								}
							}
						}
					} else {
						cancel2()
					}
				}
				lastErr = "unauthorized"
				continue
			}
			if resp.StatusCode == 403 {
				lastErr = "forbidden"
				continue
			}
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				lastErr = "http " + resp.Status
				continue
			}
			body := strings.TrimSpace(string(b))
			if body == "" {
				lastErr = "empty body"
				continue
			}

			low := strings.ToLower(body)
			if strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") {
				// Many Vnish/Anthill firmwares return SPA HTML here (not JSON API).
				// Treat as failure to avoid false "ok".
				lastErr = "html response (no json api)"
				if len(body) > 4096 {
					body = body[:4096] + "…"
				}
				out.Raw[p] = body
				continue
			}

			var m any
			sb := sanitize(b)
			if json.Unmarshal(sb, &m) == nil {
				okAny = true
				out.Responses[p] = m
			} else {
				// Not JSON, keep raw for debugging but do not count as OK.
				lastErr = "non-json response"
				out.Responses[p] = body
			}
			if len(body) > 4096 {
				body = body[:4096] + "…"
			}
			out.Raw[p] = body
		}

		out.OK = okAny
		if !okAny && lastErr != "" {
			out.Error = lastErr
		}
		return out
	}

	score := func(r ProbeResult) int {
		if !r.OK {
			return -1
		}
		f := ExtractFacts(r)
		s := 0
		s += len(r.Responses) * 10
		if f.MAC != "" {
			s += 25
		}
		if f.Model != "" {
			s += 10
		}
		if f.Firmware != "" {
			s += 10
		}
		if f.Worker != "" {
			s += 15
		}
		if f.HashrateTHS > 0 {
			s += 20
		}
		if f.UptimeS > 0 {
			s += 10
		}
		if len(f.FansRPM) > 0 {
			s += 10
		}
		if len(f.TempsC) > 0 {
			s += 10
		}
		return s
	}

	best := ProbeResult{OK: false, Error: "no endpoints succeeded (auth/blocked/offline)"}
	bestScore := -1
	lastFail := ""

	for _, c := range creds {
		for _, scheme := range schemes {
			scheme = strings.ToLower(strings.TrimSpace(scheme))
			if scheme != "http" && scheme != "https" {
				continue
			}
			rx := tryScheme(scheme, c)
			if sc := score(rx); sc > bestScore {
				bestScore = sc
				best = rx
			} else if !rx.OK && rx.Error != "" {
				lastFail = rx.Error
			}
			// stop early on strong results
			if bestScore >= 70 {
				return best
			}
		}
	}

	if !best.OK && lastFail != "" {
		best.Error = lastFail
	}
	return best
}

// ProbeAntminer tries http+https (legacy behavior). Prefer ProbeAntminerSchemes when you know ports.
func ProbeAntminer(ctx context.Context, host string, creds []Cred) ProbeResult {
	return ProbeAntminerSchemes(ctx, host, creds, []string{"http", "https"})
}

