package main

import (
	"testing"
	"time"

	"gossipnode/config/settings"
)

func TestHeaderBindingFromSettings(t *testing.T) {
	c, err := headerBindingFromSettings(settings.ConsensusSettings{
		HeaderBindingFromHeight:        500,
		CoinbaseAddress:                " 0x2222222222222222222222222222222222222222 ",
		BlockTimestampMaxFutureSeconds: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.FromHeight != 500 || c.Coinbase == nil || c.ZKVM != nil || c.MaxFutureDrift != 15*time.Second {
		t.Fatalf("got %+v", c)
	}
	if _, err := headerBindingFromSettings(settings.ConsensusSettings{ZKVMAddress: "not-an-address"}); err == nil {
		t.Fatal("bad address accepted")
	}
}
