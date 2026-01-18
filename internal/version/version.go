package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

func String() string {
	return strings.TrimSpace(raw)
}

