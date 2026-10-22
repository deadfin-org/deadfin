// SPDX-License-Identifier: GPL-2.0-only

//go:build !unix

package main

import (
	"fmt"
	"os"
)

// The wrapper needs Unix process groups. This stub keeps `go build ./...` and
// `go vet ./...` working on other systems: without a file for that case, the
// package has no buildable sources and ./... fails to load.
func main() {
	fmt.Fprintln(os.Stderr, "subreaper: Unix-only; run the command directly on this platform")
	os.Exit(1)
}
