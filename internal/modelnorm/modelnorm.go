package modelnorm

import (
	"regexp"
	"strings"
)

type Normalized struct {
	Vendor string // antminer/whatsminer/avalonminer/iceriver/elphapex/unknown
	Model  string // display
	Key    string // stable key for filtering/grouping
}

var ws = regexp.MustCompile(`\s+`)

func Normalize(raw string) Normalized {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Normalized{Vendor: "unknown", Model: "", Key: ""}
	}
	up := strings.ToUpper(s)
	up = strings.ReplaceAll(up, "_", " ")
	up = ws.ReplaceAllString(up, " ")

	sawAntminer := false

	// BTM_* is Bitmain/Antminer in many contexts
	if strings.HasPrefix(up, "BTM ") || strings.HasPrefix(up, "BTM-") || strings.HasPrefix(up, "BTM_") {
		sawAntminer = true
		up = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(up, "BTM "), "BTM-"), "BTM_"))
	}
	if strings.HasPrefix(up, "ANTMINER ") {
		sawAntminer = true
		up = strings.TrimSpace(strings.TrimPrefix(up, "ANTMINER "))
	}

	n := Normalized{Vendor: "unknown"}

	// vendor inference
	switch {
	case sawAntminer:
		n.Vendor = "antminer"
	case strings.HasPrefix(up, "M") && len(up) >= 3 && up[1] >= '0' && up[1] <= '9':
		n.Vendor = "whatsminer"
	case strings.HasPrefix(up, "A") && len(up) >= 3 && up[1] >= '0' && up[1] <= '9':
		n.Vendor = "avalonminer"
	case strings.HasPrefix(up, "KS") || strings.HasPrefix(up, "AL") || strings.HasPrefix(up, "KA"):
		// IceRiver common families: KS*, AL*, KA*
		n.Vendor = "iceriver"
	case strings.HasPrefix(up, "L") || strings.HasPrefix(up, "S"):
		// Bitmain families: L7, S19...
		n.Vendor = "antminer"
	}

	model := up
	// nicer Bitmain formatting
	if n.Vendor == "antminer" {
		// common Bitmain tokens
		model = strings.ReplaceAll(model, "  ", " ")
		model = strings.ReplaceAll(model, "J PRO", "JPRO")
		model = strings.ReplaceAll(model, "JPRO", "j Pro")
		model = strings.ReplaceAll(model, " PRO", " Pro")
		model = strings.ReplaceAll(model, "PRO", "Pro")
		model = strings.ReplaceAll(model, " PLUS", "+")
		model = strings.ReplaceAll(model, "PLUS", "+")
		// keep XP/SE/KS etc uppercase
		model = ws.ReplaceAllString(model, " ")
		model = strings.TrimSpace(model)
		if !strings.HasPrefix(strings.ToUpper(model), "ANTMINER ") {
			model = "Antminer " + model
		} else {
			model = "Antminer " + strings.TrimSpace(model[len("ANTMINER "):])
		}
	}
	if n.Vendor == "whatsminer" {
		model = "Whatsminer " + strings.TrimSpace(up)
	}
	if n.Vendor == "avalonminer" {
		model = "Avalon " + strings.TrimSpace(up)
	}
	if n.Vendor == "iceriver" {
		model = "IceRiver " + strings.TrimSpace(up)
	}

	n.Model = model
	n.Key = strings.ToUpper(ws.ReplaceAllString(strings.ReplaceAll(model, "_", " "), " "))
	return n
}

