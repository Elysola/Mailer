package main

import (
	"fmt"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

// Build metadata. `version` is overridden at link time:
//
//	go build -ldflags "-X main.version=v1.2.0"
//
// It deliberately defaults to "dev" rather than something that looks like a
// release, so an unreleased build is never mistaken for a tagged one. When the
// binary is built by `go install` or from a VCS checkout without the flag,
// buildInfoVersion recovers the module version or commit from the embedded
// build info instead of reporting "dev".
var version = "dev"

// commit and date can also be injected by the release build; fall back to
// build info when they are not.
var (
	commit = ""
	date   = ""
)

// VersionString renders the one-line version banner used by `mailer version`,
// `-version` and the startup log.
func VersionString() string {
	return fmt.Sprintf("mailer %s (%s %s/%s, %s, %s)",
		resolvedVersion(), resolvedCommit(), runtime.GOOS, runtime.GOARCH,
		runtime.Version(), resolvedDate())
}

func resolvedVersion() string {
	if version != "" && version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		// `go install module@vX.Y.Z` records the real semver here, but a
		// plain VCS build records a pseudo-version (0.0.0-20260101-abcd),
		// which is noise in a version banner. Only trust real releases.
		if v := bi.Main.Version; semverRe.MatchString(v) {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}

// semverRe matches a release tag (optionally v-prefixed, optional pre-release
// suffix) while rejecting the Go pseudo-versions a VCS build records, which
// look like 0.0.0-20260101120000-abcdef123456.
var semverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[a-zA-Z][^+]*)?(\+[0-9a-f]+)?$`)

func resolvedCommit() string {
	if commit != "" {
		return shortCommit(commit)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return shortCommit(s.Value)
			}
		}
	}
	return "unknown"
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func resolvedDate() string {
	if date != "" {
		return date
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				return s.Value
			}
		}
	}
	return "unknown"
}
