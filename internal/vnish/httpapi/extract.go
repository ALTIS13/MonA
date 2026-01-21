package httpapi

import (
	"fmt"
	"strings"
)

type Facts struct {
	Model       string
	Firmware    string
	Worker      string
	UptimeS     uint64
	HashrateTHS float64
	FansRPM     []int
	TempsC      []float64
}

// ExtractFacts tries to pull common fields from a variety of Vnish/Anthill-like JSONs.
func ExtractFacts(res ProbeResult) Facts {
	var f Facts

	// if probe guessed kind
	if res.Kind != "" {
		f.Firmware = res.Kind
	}

	// scan all json maps for some common keys
	for _, v := range res.Responses {
		f.Model = firstNonEmpty(f.Model, findStringDeep(v, set("model", "type", "miner_type", "device", "product")))
		f.Worker = firstNonEmpty(f.Worker, findStringDeep(v, set("worker", "user", "pooluser", "username")))
		if f.UptimeS == 0 {
			f.UptimeS = findU64Deep(v, set("uptime", "elapsed", "elapsed_s", "uptime_s"))
		}
		if f.HashrateTHS == 0 {
			// common: rate_5s + rate_unit (GH/s)
			hs := findF64Deep(v, set("hashrate", "rate_5s", "hashrate_5s", "hashrate5s"))
			if hs > 0 {
				unit := strings.ToLower(findStringDeep(v, set("rate_unit", "unit", "hashrate_unit")))
				if strings.Contains(unit, "gh") {
					f.HashrateTHS = hs / 1000.0
				} else if strings.Contains(unit, "th") {
					f.HashrateTHS = hs
				} else {
					// default assume GH/s for "rate_5s"
					f.HashrateTHS = hs / 1000.0
				}
			}
		}
		if len(f.FansRPM) == 0 {
			f.FansRPM = findIntSliceDeep(v, "fan", "fans", "fan_rpm", "fans_rpm")
		}
		if len(f.TempsC) == 0 {
			f.TempsC = findFloatSliceDeep(v, "temp", "temps", "temp_chip", "temp_pcb", "temperature", "temperatures")
		}
	}

	return f
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return strings.TrimSpace(b)
}

func set(keys ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, k := range keys {
		m[strings.ToLower(k)] = struct{}{}
	}
	return m
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

func findU64Deep(v any, wanted map[string]struct{}) uint64 {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, ok := wanted[kl]; ok {
				u := toU64(vv)
				if u > 0 {
					return u
				}
			}
		}
		for _, vv := range x {
			if u := findU64Deep(vv, wanted); u > 0 {
				return u
			}
		}
	case []any:
		for _, vv := range x {
			if u := findU64Deep(vv, wanted); u > 0 {
				return u
			}
		}
	}
	return 0
}

func findF64Deep(v any, wanted map[string]struct{}) float64 {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, ok := wanted[kl]; ok {
				f := toF64(vv)
				if f > 0 {
					return f
				}
			}
		}
		for _, vv := range x {
			if f := findF64Deep(vv, wanted); f > 0 {
				return f
			}
		}
	case []any:
		for _, vv := range x {
			if f := findF64Deep(vv, wanted); f > 0 {
				return f
			}
		}
	}
	return 0
}

func findIntSliceDeep(v any, keys ...string) []int {
	want := set(keys...)
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, ok := want[kl]; ok {
				if arr, ok := vv.([]any); ok {
					out := make([]int, 0, len(arr))
					for _, e := range arr {
						out = append(out, int(toU64(e)))
					}
					if len(out) > 0 {
						return out
					}
				}
			}
		}
		for _, vv := range x {
			if out := findIntSliceDeep(vv, keys...); len(out) > 0 {
				return out
			}
		}
	case []any:
		for _, vv := range x {
			if out := findIntSliceDeep(vv, keys...); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func findFloatSliceDeep(v any, keys ...string) []float64 {
	want := set(keys...)
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			kl := strings.ToLower(strings.TrimSpace(k))
			if _, ok := want[kl]; ok {
				if arr, ok := vv.([]any); ok {
					out := make([]float64, 0, len(arr))
					for _, e := range arr {
						out = append(out, toF64(e))
					}
					if len(out) > 0 {
						return out
					}
				}
			}
		}
		for _, vv := range x {
			if out := findFloatSliceDeep(vv, keys...); len(out) > 0 {
				return out
			}
		}
	case []any:
		for _, vv := range x {
			if out := findFloatSliceDeep(vv, keys...); len(out) > 0 {
				return out
			}
		}
	}
	return nil
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

