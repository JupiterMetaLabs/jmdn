package settings

import "testing"

func TestListenAddr_DefaultsToLoopback(t *testing.T) {
	if got := ListenAddr(nil, 15054); got != "127.0.0.1:15054" {
		t.Fatalf("got %s", got)
	}
}

// The block-ingest gRPC listener is checked like any other gatekeeper
// service: exposed without auth it is flagged.
func TestPosture_FlagsExposedBlockIngestGRPC(t *testing.T) {
	c := DefaultConfig()
	c.Binds.BlockGRPC = "0.0.0.0"
	found := false
	for _, v := range c.InsecurePublicServices() {
		if v.Service == ServiceBlockIngestGRPC {
			found = true
		}
	}
	if !found {
		t.Fatal("block_ingest_grpc on 0.0.0.0 with auth_type none must be flagged")
	}
}

// Compiled bind defaults keep every unauthenticated service on loopback.
func TestDefaults_NoInsecurePublicServices(t *testing.T) {
	c := DefaultConfig()
	for _, v := range c.InsecurePublicServices() {
		if v.Service != ServiceEthRPC { // public RPC is open by design, rate-limited
			t.Errorf("compiled defaults expose %s", v)
		}
	}
}
