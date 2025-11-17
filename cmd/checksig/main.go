package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"gossipnode/Security"
	"gossipnode/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

func main() {
	var rawInput string
	var fromFlag string

	flag.StringVar(&rawInput, "raw", "", "raw signed Ethereum transaction hex string (0x...)")
	flag.StringVar(&fromFlag, "from", "", "expected sender address (0x...)")
	flag.Parse()

	if rawInput == "" {
		rawInput = prompt("Enter raw signed Ethereum transaction hex: ")
	}

	rawInput = strings.TrimSpace(rawInput)
	if rawInput == "" {
		log.Fatal("no raw transaction provided")
	}

	txBytes, err := hexutil.Decode(rawInput)
	if err != nil {
		log.Fatalf("failed to decode raw transaction: %v", err)
	}

	ethTx := new(types.Transaction)
	if err := rlp.DecodeBytes(txBytes, ethTx); err != nil {
		log.Fatalf("failed to decode RLP transaction: %v", err)
	}

	configTx, err := buildConfigTransaction(ethTx, fromFlag)
	if err != nil {
		log.Fatalf("failed to build config transaction: %v", err)
	}

	fmt.Println()
	fmt.Println("Running Security.CheckSignature...")
	ok, err := Security.CheckSignature(configTx)
	if err != nil {
		log.Fatalf("signature check failed: %v", err)
	}

	fmt.Printf("Signature valid: %v\n", ok)
}

func prompt(message string) string {
	fmt.Print(message)
	reader := bufio.NewReader(os.Stdin)
	text, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("failed to read input: %v", err)
	}
	return text
}

func buildConfigTransaction(ethTx *types.Transaction, userFrom string) (*config.Transaction, error) {
	v, r, s := ethTx.RawSignatureValues()

	var fromPtr *common.Address
	if userFrom != "" {
		address := common.HexToAddress(userFrom)
		fromPtr = &address
		fmt.Printf("Using sender address from flag: %s\n", address.Hex())
	} else {
		if derived, err := deriveSender(ethTx); err == nil {
			fromPtr = &derived
			fmt.Printf("Derived sender address from signature: %s\n", derived.Hex())
		} else {
			return nil, fmt.Errorf("could not derive sender address, use -from flag: %w", err)
		}
	}

	to := ethTx.To()
	if to != nil {
		fmt.Printf("Transaction to address: %s\n", to.Hex())
	} else {
		fmt.Println("Transaction is contract creation (no To address)")
	}

	configTx := &config.Transaction{
		Hash:           ethTx.Hash(),
		From:           fromPtr,
		To:             to,
		Value:          ethTx.Value(),
		Type:           ethTx.Type(),
		ChainID:        ethTx.ChainId(),
		Nonce:          ethTx.Nonce(),
		GasLimit:       ethTx.Gas(),
		GasPrice:       ethTx.GasPrice(),
		MaxFee:         ethTx.GasFeeCap(),
		MaxPriorityFee: ethTx.GasTipCap(),
		Data:           ethTx.Data(),
		AccessList:     toConfigAccessList(ethTx.AccessList()),
		V:              v,
		R:              r,
		S:              s,
	}

	if ethTx.Type() == types.LegacyTxType {
		configTx.MaxFee = nil
		configTx.MaxPriorityFee = nil
	}

	return configTx, nil
}

func deriveSender(ethTx *types.Transaction) (common.Address, error) {
	chainID := ethTx.ChainId()
	signer := types.LatestSignerForChainID(chainID)

	from, err := types.Sender(signer, ethTx)
	if err == nil {
		return from, nil
	}

	// fallback attempts mirroring Security.CheckSignature behaviour
	if v := ethTx.Type(); v == types.LegacyTxType {
		if from, err2 := types.Sender(types.HomesteadSigner{}, ethTx); err2 == nil {
			return from, nil
		}
		if chainID != nil {
			if from, err3 := types.Sender(types.NewEIP155Signer(chainID), ethTx); err3 == nil {
				return from, nil
			}
		}
	}

	return common.Address{}, err
}

func toConfigAccessList(accessList types.AccessList) config.AccessList {
	var result config.AccessList
	for _, tuple := range accessList {
		result = append(result, config.AccessTuple{
			Address:     tuple.Address,
			StorageKeys: append([]common.Hash{}, tuple.StorageKeys...),
		})
	}
	return result
}
