package version

import (
	"fmt"
	"runtime"
)

// ClientName is the canonical public-facing name for this node software.
// Used in web3_clientVersion, health endpoints, and anywhere the node
// identifies itself to external tooling (block explorers, wallets, crawlers).
// Defined once here — every call site references this constant.
const ClientName = "JMDT"

// Build information. Private variables populated at build-time via -ldflags.
var (
	gitTag    string
	gitBranch string
	gitCommit string
	buildTime string
)

// VersionInfo represents the version information structure
type VersionInfo struct {
	GitTag    string `json:"git_tag"`
	GitBranch string `json:"git_branch"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
}

// GetVersionInfo returns the populated VersionInfo struct
func GetVersionInfo() VersionInfo {
	return VersionInfo{
		GitTag:    gitTag,
		GitBranch: gitBranch,
		GitCommit: gitCommit,
		BuildTime: buildTime,
		GoVersion: runtime.Version(),
	}
}

// String returns a formatted version string for logs and CLI output.
func String() string {
	return fmt.Sprintf("Tag: %s, Branch: %s, Commit: %s, Built: %s, Go: %s",
		gitTag, gitBranch, gitCommit, buildTime, runtime.Version())
}

// ClientVersion returns the Ethereum-standard web3_clientVersion string.
// Uses the package-level ClientName constant — no magic strings at call sites.
// Format: ClientName/Version/OS-Arch/GoVersion
//
// Examples:
//
//	JMDN/v1.1.0-f984673/linux-amd64/go1.25.1    (tagged release, clean)
//	JMDN/v1.1.0-5-gf984673-dirty/darwin-arm64/go1.25.1  (dev build)
//	JMDN/dev-f984673/linux-amd64/go1.25.1        (untagged commit)
//	JMDN/unknown/linux-amd64/go1.25.1             (no git info)
func ClientVersion() string {
	tag := versionTag()
	return fmt.Sprintf("%s/%s/%s-%s/%s",
		ClientName, tag, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// versionTag returns the best available version identifier.
// Prefers gitTag (set by `git describe` via ldflags), falls back to
// "dev-<commit>" for untagged builds, and "unknown" as last resort.
func versionTag() string {
	if gitTag != "" && gitTag != "unknown" {
		return gitTag
	}
	if gitCommit != "" {
		return "dev-" + gitCommit
	}
	return "unknown"
}
