//go:build !mikrotik
// +build !mikrotik

package main

import "fmt"

func main() {
	fmt.Println("mikrotik module is disabled in this build (build with: go run -tags mikrotik ./cmd/mikrotik)")
}

