package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

func main() {
	// Connect to Geth node (HTTP or WS)
	client, err := ethclient.Dial("http://192.168.100.24:32004")
	if err != nil {
		log.Fatalf("Failed to connect to the Ethereum client: %v", err)
	}
	defer client.Close()

	// Load private key
	privateKey, err := crypto.HexToECDSA("b2299edadf87bf42660e1cd3f8dbd3872a1830fc9a8a8aa129a7f4dd0df4a178") // hex without 0x
	if err != nil {
		log.Fatalf("Failed to load private key: %v", err)
	}

	// Derive sender address
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		log.Fatalf("Cannot assert type: publicKey is not of type *ecdsa.PublicKey")
	}
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)
	fmt.Println("Sender address:", fromAddress.Hex())

	// Get nonce
	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		log.Fatalf("Failed to get nonce: %v", err)
	}

	// Set transaction values
	value := new(big.Int)
	value, _ = value.SetString("1000000000000000000000", 10) // 1000 * 10^18 (like parseEther("1000"))

	gasLimit := uint64(21000) // basic transfer
	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		log.Fatalf("Failed to get gas price: %v", err)
	}

	toAddress := common.HexToAddress("0xCCf5ED35048c642330705C3ab485c10101F4aB4F")
	tx := types.NewTransaction(nonce, toAddress, value, gasLimit, gasPrice, nil)

	// Sign transaction
	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		log.Fatalf("Failed to get chain ID: %v", err)
	}
	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		log.Fatalf("Failed to sign transaction: %v", err)
	}

	// Send transaction
	err = client.SendTransaction(context.Background(), signedTx)
	if err != nil {
		log.Fatalf("Failed to send transaction: %v", err)
	}

	fmt.Println("Transaction hash:", signedTx.Hash().Hex())

	// Wait for mining
	receipt, err := bind.WaitMined(context.Background(), client, signedTx)
	if err != nil {
		log.Fatalf("Failed to wait for mining: %v", err)
	}
	fmt.Println("Transaction mined in block:", receipt.BlockNumber.Uint64())
}