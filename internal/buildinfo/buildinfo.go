// SPDX-License-Identifier: GPL-2.0-only

// Package buildinfo carries the immutable server build identity.
// The checked-in file is the unknown/local default. tools/buildinfo-gen
// stages a replacement out of tree and go build consumes it via -overlay.
package buildinfo

const (
	Component           = "deadfin-server"
	Version             = "0.0.0-local"
	Channel             = "local"
	Revision            = "unknown"
	Dirty               = true
	BuildTime           = ""
	CompatibilityTarget = "12.1.0"
	APIVersion          = 1
)

// Capabilities returns a fresh slice on every call.
func Capabilities() []string { return []string{"build-info"} }
