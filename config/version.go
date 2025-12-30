package config

import (
	"fmt"
	"runtime"
)

// Build information. Populated at build-time.
var (
	GitCommit string
	GitBranch string
	GitTag    string
	BuildTime string
)

// VersionString returns a formatted version string
func VersionString() string {
	return fmt.Sprintf("Tag: %s, Branch: %s, Commit: %s, Built: %s, Go: %s",
		GitTag, GitBranch, GitCommit, BuildTime, runtime.Version())
}
