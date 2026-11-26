package Service

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"gossipnode/config/version"
)

// web3_clientVersion must report the build (config/version), not a fixed
// string: tooling and the seed dashboard read the node's build from it.
func TestClientVersionReportsBuild(t *testing.T) {
	got, err := (&ServiceImpl{}).ClientVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != version.ClientVersion() {
		t.Fatalf("ClientVersion() = %q, want version.ClientVersion() = %q", got, version.ClientVersion())
	}
	parts := strings.Split(got, "/")
	if len(parts) != 4 || parts[0] != version.ClientName {
		t.Fatalf("ClientVersion() = %q, want %s/<tag>/<os>-<arch>/<go>", got, version.ClientName)
	}
	if parts[2] != runtime.GOOS+"-"+runtime.GOARCH || parts[3] != runtime.Version() {
		t.Fatalf("ClientVersion() = %q: platform or Go version wrong", got)
	}
	if got == "JMDT/v1.0.0" {
		t.Fatal("ClientVersion() is the old hardcoded string")
	}
}
