package httpapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type ProbeResult struct {
	OK       bool              `json:"ok"`
	Scheme   string            `json:"scheme,omitempty"`
	UsedCred string            `json:"used_cred,omitempty"`
	Error    string            `json:"error,omitempty"`
	Kind     string            `json:"kind,omitempty"` // "anthill" / "vnish" / "unknown"
	// endpoint -> parsed json or raw string
	Responses map[string]any    `json:"responses,omitempty"`
	Raw       map[string]string `json:"raw,omitempty"`
}

type Cred struct {
	Name     string
	Username string
	Password string
}

// Probe tries to access Vnish/Anthill-style JSON APIs.
// Many builds are SPAs; the data is usually exposed via /api/* endpoints with cookie session.
func Probe(ctx context.Context, host string, creds []Cred, schemes []string) ProbeResult {
	if len(schemes) == 0 {
		schemes = []string{"http"}
	}
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 1500 * time.Millisecond, KeepAlive: -1}).DialContext,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: 0,
		IdleConnTimeout:     0,
		TLSHandshakeTimeout: 900 * time.Millisecond,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec
			MinVersion:         tls.VersionTLS10,
		},
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: tr, Jar: jar}

	sanitize := func(b []byte) []byte {
		s := string(b)
		if i := strings.Index(s, "{"); i >= 0 {
			return []byte(strings.TrimSpace(s[i:]))
		}
		if i := strings.Index(s, "["); i >= 0 {
			return []byte(strings.TrimSpace(s[i:]))
		}
		return b
	}

	// Candidate endpoints observed across Vnish/Anthill-like UIs.
	// We try them best-effort; many will 404.
	dataEndpoints := []string{
		"/api/v1/summary",
		"/api/v1/stats",
		"/api/v1/status",
		"/api/summary",
		"/api/stats",
		"/api/status",
		"/api/miner/summary",
		"/api/miner/stats",
		"/api/system/info",
		"/api/info",
	}

	loginAttempts := []struct {
		path string
		kind string // json or form
	}{
		{path: "/api/login", kind: "json"},
		{path: "/api/v1/login", kind: "json"},
		{path: "/auth/login", kind: "json"},
		{path: "/login", kind: "form"},
	}

	tryFetch := func(ctx context.Context, scheme, path string) (any, string, int, error) {
		u := scheme + "://" + host + path
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		req.Close = true
		req.Header.Set("Connection", "close")
		req.Header.Set("User-Agent", "MonA/asic-control")
		req.Header.Set("Accept", "application/json,text/plain;q=0.9,*/*;q=0.8")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", 0, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		_ = resp.Body.Close()
		body := strings.TrimSpace(string(b))
		raw := body
		if len(raw) > 4096 {
			raw = raw[:4096] + "…"
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, raw, resp.StatusCode, nil
		}
		low := strings.ToLower(body)
		if strings.HasPrefix(low, "<!doctype html") || strings.HasPrefix(low, "<html") {
			// Not JSON API response.
			return nil, raw, resp.StatusCode, nil
		}
		var m any
		sb := sanitize(b)
		if json.Unmarshal(sb, &m) == nil {
			return m, raw, resp.StatusCode, nil
		}
		return nil, raw, resp.StatusCode, nil
	}

	tryLogin := func(scheme string, cred Cred) bool {
		for _, att := range loginAttempts {
			u := scheme + "://" + host + att.path
			var req *http.Request
			if att.kind == "json" {
				payload := map[string]string{"username": cred.Username, "password": cred.Password}
				b, _ := json.Marshal(payload)
				req, _ = http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
				req.Header.Set("content-type", "application/json")
			} else {
				form := url.Values{}
				form.Set("username", cred.Username)
				form.Set("password", cred.Password)
				req, _ = http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(form.Encode()))
				req.Header.Set("content-type", "application/x-www-form-urlencoded")
			}
			req.Close = true
			req.Header.Set("Connection", "close")
			req.Header.Set("User-Agent", "MonA/asic-control")
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			_, _ = io.ReadAll(io.LimitReader(resp.Body, 32*1024))
			_ = resp.Body.Close()
			// Common: 200 OK with cookie, or 302 redirect with cookie.
			if resp.StatusCode == 200 || resp.StatusCode == 204 || resp.StatusCode == 302 || resp.StatusCode == 303 {
				return true
			}
		}
		return false
	}

	score := func(res ProbeResult) int {
		if !res.OK {
			return -1
		}
		f := ExtractFacts(res)
		s := 0
		s += len(res.Responses) * 10
		if f.Model != "" {
			s += 15
		}
		if f.Firmware != "" {
			s += 10
		}
		if f.Worker != "" {
			s += 10
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

	best := ProbeResult{OK: false, Error: "no vnish/anthill json endpoints succeeded", Responses: map[string]any{}, Raw: map[string]string{}}
	bestScore := -1

	for _, scheme := range schemes {
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		if scheme != "http" && scheme != "https" {
			continue
		}

		for _, cred := range creds {
			// reset cookies for each cred attempt
			jar, _ := cookiejar.New(nil)
			client.Jar = jar

			// optional login
			_ = tryLogin(scheme, cred)

			out := ProbeResult{Responses: map[string]any{}, Raw: map[string]string{}, Scheme: scheme, UsedCred: cred.Name}
			okAny := false

			// fetch data endpoints
			for _, ep := range dataEndpoints {
				reqCtx, cancel := context.WithTimeout(ctx, 1600*time.Millisecond)
				m, raw, code, err := tryFetch(reqCtx, scheme, ep)
				cancel()
				if err != nil {
					continue
				}
				if raw != "" {
					out.Raw[ep] = raw
				}
				if m != nil {
					out.Responses[ep] = m
					okAny = true
					// guess kind from HTML meta in raw if present
					if strings.Contains(strings.ToLower(raw), `meta name="firmware"`) && strings.Contains(strings.ToLower(raw), "anthillos") {
						out.Kind = "anthill"
					}
				} else if code == 401 || code == 403 {
					// keep trying other endpoints/login paths
				}
			}

			out.OK = okAny
			if !okAny {
				out.Error = "unauthorized or no json endpoints"
			}

			if sc := score(out); sc > bestScore {
				bestScore = sc
				best = out
			}
			if bestScore >= 70 {
				return best
			}
		}
	}

	return best
}

