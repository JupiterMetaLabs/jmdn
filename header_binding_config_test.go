package main

import (
	"testing"
	"time"

	"gossipnode/config/settings"
)

const (
	cfgCB = "0x2222222222222222222222222222222222222222"
	cfgZK = "0x3333333333333333333333333333333333333333"
)

func TestHeaderBindingFromSettings(t *testing.T) {
	c, err := headerBindingFromSettings(settings.ConsensusSettings{
		HeaderBindingFromHeight: 500,
		HeaderAddresses: []settings.HeaderAddressRange{
			{FromHeight: 500, Coinbase: " " + cfgCB + " ", ZKVM: cfgZK},
			{FromHeight: 900, Coinbase: cfgZK, ZKVM: cfgCB},
		},
		BlockTimestampMaxFutureSeconds: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.FromHeight != 500 || len(c.Addresses) != 2 || c.MaxFutureDrift != 15*time.Second {
		t.Fatalf("got %+v", c)
	}
}

func TestHeaderBindingFromSettings_Refuses(t *testing.T) {
	for name, cs := range map[string]settings.ConsensusSettings{
		"binding on, no addresses": {HeaderBindingFromHeight: 500},
		"first range starts above the height": {HeaderBindingFromHeight: 500,
			HeaderAddresses: []settings.HeaderAddressRange{{FromHeight: 600, Coinbase: cfgCB, ZKVM: cfgZK}}},
		"empty zkvm": {HeaderBindingFromHeight: 500,
			HeaderAddresses: []settings.HeaderAddressRange{{FromHeight: 1, Coinbase: cfgCB}}},
		"bad address": {HeaderAddresses: []settings.HeaderAddressRange{{Coinbase: "not-an-address", ZKVM: cfgZK}}},
		"not increasing": {HeaderBindingFromHeight: 1, HeaderAddresses: []settings.HeaderAddressRange{
			{FromHeight: 1, Coinbase: cfgCB, ZKVM: cfgZK}, {FromHeight: 1, Coinbase: cfgCB, ZKVM: cfgZK}}},
	} {
		if _, err := headerBindingFromSettings(cs); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if _, err := headerBindingFromSettings(settings.ConsensusSettings{}); err != nil {
		t.Fatalf("binding off with no addresses: %v", err)
	}
}
