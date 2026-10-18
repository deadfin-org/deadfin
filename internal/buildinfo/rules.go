// SPDX-License-Identifier: GPL-2.0-only

package buildinfo

import (
	"errors"
	"fmt"
	"strings"
)

// Identity is the validated subset of the build identity. The generator and
// the compiled constants both pass through Validate, so the rejection rules
// of the build identity exist once.
type Identity struct {
	Component string
	Version   string
	Channel   string
	Revision  string
	Dirty     bool
	BuildTime string
}

// Current returns the compiled constants.
func Current() Identity {
	return Identity{
		Component: Component,
		Version:   Version,
		Channel:   Channel,
		Revision:  Revision,
		Dirty:     Dirty,
		BuildTime: BuildTime,
	}
}

const (
	// ChannelRelease through ChannelLocal are the only accepted channels.
	ChannelRelease = "release"
	ChannelRC      = "rc"
	ChannelTrain   = "train"
	ChannelMaster  = "master"
	ChannelDev     = "dev"
	ChannelLocal   = "local"

	// LocalVersion is the only version a local channel may carry.
	LocalVersion = "0.0.0-local"

	// UnknownRevision marks a build whose commit could not be determined.
	UnknownRevision = "unknown"
)

// IsRevision reports whether s is 40 lowercase hex characters (rule 2).
func IsRevision(s string) bool { return len(s) == 40 && isHex(s) }

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// isVersionCore matches ^v?\d+\.\d+\.\d+$ (rule 5).
func isVersionCore(s string) bool {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	return len(parts) == 3 && isDigits(parts[0]) && isDigits(parts[1]) && isDigits(parts[2])
}

// isRCVersion matches ^\d+\.\d+\.\d+-rc\.[1-9]\d*$ (rule 13). The
// leading v is absent: tools/image-publication strips it for rc tags.
func isRCVersion(s string) bool {
	core, n, ok := strings.Cut(s, "-rc.")
	return ok && !strings.HasPrefix(core, "v") && isVersionCore(core) && isDigits(n) && n[0] != '0'
}

// isDevSuffix matches -dev-[0-9a-f]{12} at the end of s and returns the core.
func devSuffix(s string) (core string, ok bool) {
	const tail = 12
	i := strings.LastIndex(s, "-dev-")
	if i < 0 || len(s)-(i+len("-dev-")) != tail {
		return "", false
	}
	return s[:i], isHex(s[i+len("-dev-"):])
}

// isTrainVersion matches ^v?\d+\.\d+\.\d+-dev-[0-9a-f]{12}$ (rule 6).
func isTrainVersion(s string) bool {
	core, ok := devSuffix(s)
	return ok && isVersionCore(core)
}

// isDevVersion matches ^\d+\.\d+\.0-dev\.\d{14}-[0-9a-f]{12}$ (rule 14):
// the release line, a UTC commit timestamp, and the commit prefix.
func isDevVersion(s string) bool {
	core, rest, ok := strings.Cut(s, "-dev.")
	if !ok || strings.HasPrefix(core, "v") || !isVersionCore(core) || !strings.HasSuffix(core, ".0") {
		return false
	}
	stamp, sha12, ok := strings.Cut(rest, "-")
	return ok && len(stamp) == 14 && isDigits(stamp) && len(sha12) == 12 && isHex(sha12)
}

// isMasterVersion matches ^dev-[0-9a-f]{12}$ (rule 7).
func isMasterVersion(s string) bool {
	core, ok := devSuffix("-" + s)
	return ok && core == ""
}

// Validate applies the build identity rejection rules. The version rule
// accepts an optional leading v (the spelling
// tools/image-publication emits for train/vX.Y.Z).
func Validate(id Identity) error {
	if id.Component != Component {
		return fmt.Errorf("component %q is not %q", id.Component, Component)
	}
	switch id.Channel {
	case ChannelRelease, ChannelRC, ChannelTrain, ChannelMaster, ChannelDev, ChannelLocal:
	default:
		return fmt.Errorf("rule 1: channel %q is not one of release, rc, train, master, dev, local", id.Channel)
	}
	if id.Revision != UnknownRevision && !IsRevision(id.Revision) {
		return fmt.Errorf("rule 2: revision %q is neither unknown nor 40 lowercase hex characters", id.Revision)
	}
	if id.Channel != ChannelLocal && id.Revision == UnknownRevision {
		return fmt.Errorf("rule 3: channel %s requires a known revision", id.Channel)
	}
	if (id.Channel == ChannelRelease || id.Channel == ChannelRC) && id.Dirty {
		return fmt.Errorf("rule 4: a %s build cannot be dirty", id.Channel)
	}
	switch id.Channel {
	case ChannelRelease:
		if !isVersionCore(id.Version) {
			return fmt.Errorf("rule 5: release version %q does not match ^v?X.Y.Z$", id.Version)
		}
	case ChannelRC:
		if !isRCVersion(id.Version) {
			return fmt.Errorf("rule 13: rc version %q does not match ^X.Y.Z-rc.N$", id.Version)
		}
	case ChannelTrain:
		if !isTrainVersion(id.Version) || !strings.HasPrefix(id.Revision, id.Version[len(id.Version)-12:]) {
			return fmt.Errorf("rule 6: train version %q does not match ^v?X.Y.Z-dev-<sha12>$ with the suffix a prefix of revision %q", id.Version, id.Revision)
		}
	case ChannelMaster:
		if !isMasterVersion(id.Version) || !strings.HasPrefix(id.Revision, id.Version[len(id.Version)-12:]) {
			return fmt.Errorf("rule 7: master version %q does not match ^dev-<sha12>$ with the suffix a prefix of revision %q", id.Version, id.Revision)
		}
	case ChannelDev:
		if !isDevVersion(id.Version) || !strings.HasPrefix(id.Revision, id.Version[len(id.Version)-12:]) {
			return fmt.Errorf("rule 14: dev version %q does not match ^X.Y.0-dev.<yyyymmddHHMMSS>-<sha12>$ with the suffix a prefix of revision %q", id.Version, id.Revision)
		}
	case ChannelLocal:
		if id.Version != LocalVersion {
			return fmt.Errorf("rule 8: local version %q is not %q", id.Version, LocalVersion)
		}
	}
	if id.Revision == UnknownRevision && !id.Dirty {
		return errors.New("rule 12: an unknown revision is never clean")
	}
	return nil
}
