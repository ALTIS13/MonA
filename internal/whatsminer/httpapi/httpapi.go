package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type ProbeResult struct {
	OK       bool              `json:"ok"`
	Scheme   string            `json:"scheme,omitempty"`
	UsedCred string            `json:"used_cred,omitempty"`
	Error    string            `json:"error,omitempty"`
	Responses map[string]any   `json:"responses,omitempty"`
	Raw      map[string]string `json:"raw,omitempty"`
}

type Cred struct {
	Name     string
	Username string
	Password string
}

func Probe(ctx context.Context, host string, creds []Cred, schemes []string) ProbeResult {
	if len(schemes) == 0 {
		schemes = []string{"http"}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 1800 * time.Millisecond, KeepAlive: -1}).DialContext,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        0,
		MaxIdleConnsPerHost: 0,
		IdleConnTimeout:     0,
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: tr}

	endpoints := []string{
		"/cgi-bin/get_miner_status.cgi",
		"/cgi-bin/get_system_info.cgi",
		"/cgi-bin/summary.cgi",
	}

	best := ProbeResult{OK: false, Error: "no whatsminer json endpoints succeeded", Responses: map[string]any{}, Raw: map[string]string{}}
	bestScore := -1

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

	for _, scheme := range schemes {
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		if scheme != "http" && scheme != "https" {
			continue
		}
		for _, c := range creds {
			out := ProbeResult{Responses: map[string]any{}, Raw: map[string]string{}, Scheme: scheme, UsedCred: c.Name}
			okAny := false
			lastErr := ""
			for _, p := range endpoints {
				reqCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
				url := scheme + "://" + host + p
				req, _ := http.NewRequestWithContext(reqCtx, "GET", url, nil)
				req.Close = true
				req.Header.Set("Connection", "close")
				req.Header.Set("User-Agent", "MonA/asic-control")
				req.Header.Set("Accept", "application/json,text/plain;q=0.9,*/*;q=0.8")
				if c.Username != "" || c.Password != "" {
					req.SetBasicAuth(c.Username, c.Password)
				}
				resp, err := client.Do(req)
				if err != nil {
					lastErr = err.Error()
					cancel()
					continue
				}
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
				_ = resp.Body.Close()
				cancel()

				if resp.StatusCode == 401 || resp.StatusCode == 403 {
					lastErr = "unauthorized"
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

				var m any
				if json.Unmarshal(b, &m) == nil {
					out.Responses[p] = m
					okAny = true
				} else {
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
			if sc := score(out); sc > bestScore {
				bestScore = sc
				best = out
			}
		}
	}

	return best
}

