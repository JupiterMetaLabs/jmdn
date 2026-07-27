// Command genmnemonic generates a fresh, cryptographically-random BIP39 mnemonic
// for use as JMDN_NODE_SELECTION_MNEMONIC (the secret VRF selection key).
//
// It reuses the node's own generator (selection.GenerateNewMnemonic, 256-bit
// entropy / 24 words) and round-trips it through GenerateKeysFromMnemonic so an
// invalid phrase can never be printed.
//
// Usage:
//
//	go run ./Scripts/genmnemonic            # prints the mnemonic (one line)
//	go run ./Scripts/genmnemonic --export   # prints an `export ...` line
//
// SECURITY: this is a SECRET. Do not commit it, paste it into chat/logs, or
// echo it into shell history. Pipe it straight into your secret store, e.g.:
//
//	go run ./Scripts/genmnemonic | your-secrets-cli set JMDN_NODE_SELECTION_MNEMONIC
package main

import (
	"flag"
	"fmt"
	"os"

	"gossipnode/AVC/NodeSelection/pkg/selection"
)

func main() {
	export := flag.Bool("export", false, "print as an `export JMDN_NODE_SELECTION_MNEMONIC=...` line")
	flag.Parse()

	mnemonic, err := selection.GenerateNewMnemonic()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate mnemonic:", err)
		os.Exit(1)
	}

	// Round-trip: prove the phrase derives valid selection keys before emitting.
	if _, _, err := selection.GenerateKeysFromMnemonic(mnemonic); err != nil {
		fmt.Fprintln(os.Stderr, "generated mnemonic failed validation:", err)
		os.Exit(1)
	}

	if *export {
		fmt.Printf("export JMDN_NODE_SELECTION_MNEMONIC=%q\n", mnemonic)
		return
	}
	fmt.Println(mnemonic)
}
