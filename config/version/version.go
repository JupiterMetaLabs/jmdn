package version

import (
	"fmt"
	"runtime"
)

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

// String returns a formatted version string
func String() string {
	return fmt.Sprintf("Tag: %s, Branch: %s, Commit: %s, Built: %s, Go: %s",
		gitTag, gitBranch, gitCommit, buildTime, runtime.Version())
}
