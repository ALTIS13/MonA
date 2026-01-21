package httpapi

import (
	"fmt"
	"strings"
)

type Facts struct {
	MAC         string
	Model       string
	Firmware    string
	Worker      string
	UptimeS     uint64
	HashrateTHS float64
	FansRPM     []int
	TempsC      []float64
}

// ExtractFacts tries to pull common fields out of Antminer /cgi-bin JSON responses.
// It is intentionally tolerant: different firmwares (stock/vnish/custom) vary a lot.
func ExtractFacts(res ProbeResult) Facts {
	var f Facts

	// Antminer stock JSON often includes INFO.type in summary/stats.
	if anyv, ok := res.Responses["/cgi-bin/summary.cgi"]; ok {
		if m, ok := anyv.(map[string]any); ok {
			if info, ok := m["INFO"].(map[string]any); ok && f.Model == "" {
				if t, ok := info["type"].(string); ok && strings.TrimSpace(t) != "" {
					f.Model = strings.TrimSpace(t)
				}
			}
		}
	}
	if anyv, ok := res.Responses["/cgi-bin/stats.cgi"]; ok {
		if m, ok := anyv.(map[string]any); ok {
			if info, ok := m["INFO"].(map[string]any); ok && f.Model == "" {
				if t, ok := info["type"].(string); ok && strings.TrimSpace(t) != "" {
					f.Model = strings.TrimSpace(t)
				}
			}
		}
	}

	// get_system_info.cgi is usually the most reliable for model/fw/mac.
	if anyv, ok := res.Responses["/cgi-bin/get_system_info.cgi"]; ok {
		if m, ok := anyv.(map[string]any); ok {
			f.Model = pickString(m, "model", "Model", "miner_type", "type", "minerType", "minertype", "minerType")
			f.Firmware = pickString(m, "fw_ver", "firmware", "Firmware", "version", "miner_version", "minerVersion")
			f.MAC = normalizeMAC(pickString(m, "mac", "Mac", "macaddr", "mac_addr", "mac_address", "MAC", "MacAddr"))
			if f.MAC == "" {
				f.MAC = normalizeMAC(findStringDeep(m, map[string]struct{}{"mac": {}, "macaddr": {}, "mac_address": {}, "macaddr0": {}, "mac0": {}}))
			}
		}
	}
	// Heuristic: some firmwares report model as "Antminer SOC". Try to recover real model from raw.
	if f.Model == "" || strings.Contains(strings.ToUpper(f.Model), "SOC") {
		raw := ""
		for _, v := range res.Raw {
			raw += " " + strings.ToUpper(v)
		}
		switch {
		case strings.Contains(raw, "S21 PRO"):
			f.Model = "Antminer S21 Pro"
		case strings.Contains(raw, " S21"):
			f.Model = "Antminer S21"
		case strings.Contains(raw, " S19"):
			f.Model = "Antminer S19"
		case strings.Contains(raw, " L7"):
			f.Model = "Antminer L7"
		case strings.Contains(raw, " KS5 PRO") || strings.Contains(raw, " KS5PRO"):
			f.Model = "Antminer KS5 Pro"
		case strings.Contains(raw, " KS5"):
			f.Model = "Antminer KS5"
		}
	}

	// summary.cgi often exposes hashrate + uptime (varies)
	if anyv, ok := res.Responses["/cgi-bin/summary.cgi"]; ok {
		if m, ok := anyv.(map[string]any); ok {
			// common cgminer-like: {"SUMMARY":[{...}], "STATUS":[...]}
			sumObj := m
			if v, ok := m["SUMMARY"]; ok {
				if mm, ok := firstMap(v); ok {
					sumObj = mm
				}
			}
			if f.UptimeS == 0 {
				f.UptimeS = pickU64(sumObj, "Elapsed", "elapsed", "uptime", "Uptime", "time")
			}
			if f.HashrateTHS == 0 {
				// common units: GHS or MHS
				// stock newer firmwares: rate_5s + rate_unit (GH/s)
				if v, ok := sumObj["rate_5s"]; ok {
					unit := ""
					if u, ok := sumObj["rate_unit"].(string); ok {
						unit = strings.ToLower(strings.TrimSpace(u))
					}
					x := toF64(v)
					switch {
					case strings.Contains(unit, "gh"):
						f.HashrateTHS = x / 1000.0
					case strings.Contains(unit, "th"):
						f.HashrateTHS = x
					default:
						// assume GH/s by default for this field
						f.HashrateTHS = x / 1000.0
					}
				} else if v, ok := sumObj["GHS 5s"]; ok {
					f.HashrateTHS = toF64(v) / 1e3
				} else if v, ok := sumObj["GHS av"]; ok {
					f.HashrateTHS = toF64(v) / 1e3
				} else if v, ok := sumObj["MHS 5s"]; ok {
					f.HashrateTHS = toF64(v) / 1e6
				} else if v, ok := sumObj["MHS av"]; ok {
					f.HashrateTHS = toF64(v) / 1e6
				} else if v, ok := sumObj["hashrate"]; ok {
					// unknown unit, assume TH/s if looks reasonable
					f.HashrateTHS = toF64(v)
				}
			}
		}
	}

	// stats.cgi often contains fan/temp and sometimes pool user.
	if anyv, ok := res.Responses["/cgi-bin/stats.cgi"]; ok {
		if m, ok := anyv.(map[string]any); ok {
			// worker / pool user (deep search)
			if f.Worker == "" {
				f.Worker = findStringDeep(m, map[string]struct{}{
					"user": {}, "pooluser": {}, "pool_user": {}, "miner_user": {}, "worker": {}, "username": {},
				})
			}
			// common cgminer-like: {"STATS":[{...},{...}]}
			var statMaps []map[string]any
			if v, ok := m["STATS"]; ok {
				statMaps = collectMaps(v)
			} else {
				statMaps = []map[string]any{m}
			}

			fans := map[int]int{}
			temps := map[int]float64{}
			for _, sm := range statMaps {
				// newer firmwares: fan: [..]
				if fv, ok := sm["fan"]; ok {
					if arr, ok := fv.([]any); ok {
						for i, x := range arr {
							fans[i+1] = int(toU64(x))
						}
					}
				}
				// temps nested per chain: temp_chip/temp_pcb arrays
				if ch, ok := sm["chain"].([]any); ok {
					for _, cx := range ch {
						cm, ok := cx.(map[string]any)
						if !ok {
							continue
						}
						maxChip := 0.0
						if tc, ok := cm["temp_chip"].([]any); ok {
							for _, t := range tc {
								if v := toF64(t); v > maxChip {
									maxChip = v
								}
							}
						}
						if maxChip > 0 {
							temps[len(temps)+1] = maxChip
						}
					}
				}
				for k, v := range sm {
					kl := strings.ToLower(strings.TrimSpace(k))
					if strings.HasPrefix(kl, "fan") {
						if n, ok := parseSuffixInt(kl, "fan"); ok {
							fans[n] = int(toU64(v))
						}
					}
					if strings.HasPrefix(kl, "temp") {
						if n, ok := parseSuffixInt(kl, "temp"); ok {
							temps[n] = toF64(v)
						}
					}
				}
			}
			if len(fans) > 0 {
				f.FansRPM = denseInts(fans, 1, 8)
			}
			if len(temps) > 0 {
				f.TempsC = denseFloats(temps, 1, 12)
			}
		}
	}

	return f
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func pickU64(m map[string]any, keys ...string) uint64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			u := toU64(v)
			if u > 0 {
				return u
			}
		}
	}
	return 0
}

func firstMap(v any) (map[string]any, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		// sometimes it's []map[string]any
		if mm, ok := v.([]map[string]any); ok && len(mm) > 0 {
			return mm[0], true
		}
		return nil, false
	}
	m, ok := arr[0].(map[string]any)
	return m, ok
}

func collectMaps(v any) []map[string]any {
	var out []map[string]any
	if mm, ok := v.([]map[string]any); ok {
		return mm
	}
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func findStringDeep(v any, wanted map[string]struct{}) string {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, ok := wanted[kl]; ok {
				if s, ok := vv.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						return s
					}
				}
			}
		}
		for _, vv := range x {
			if s := findStringDeep(vv, wanted); s != "" {
				return s
			}
		}
	case []any:
		for _, vv := range x {
			if s := findStringDeep(vv, wanted); s != "" {
				return s
			}
		}
	case []map[string]any:
		for _, m := range x {
			if s := findStringDeep(m, wanted); s != "" {
				return s
			}
		}
	}
	return ""
}

func normalizeMAC(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}
	// allow "aa:bb:cc:dd:ee:ff" or "aa-bb-cc-dd-ee-ff"
	s = strings.ReplaceAll(s, "-", ":")
	return s
}

func parseSuffixInt(s, prefix string) (int, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	prefix = strings.ToLower(prefix)
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(s, prefix)
	if rest == "" {
		return 0, false
	}
	n := 0
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

