package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	wsURL   = "ws://192.168.100.24:32004" // Geth WebSocket endpoint
	logFile = "geth_events.log"
)

func initLogger() *os.File {
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("cannot open log file: %v", err)
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	// also log to console
	log.SetOutput(&multiWriter{files: []*os.File{f}, withConsole: true})
	return f
}

type multiWriter struct {
	files       []*os.File
	withConsole bool
}

func (mw *multiWriter) Write(p []byte) (int, error) {
	for _, f := range mw.files {
		_, _ = f.Write(p)
	}
	if mw.withConsole {
		fmt.Print(string(p))
	}
	return len(p), nil
}

func weiToEther(v *big.Int) string {
	// simple formatter
	f := new(big.Rat).SetFrac(v, big.NewInt(1e18))
	return f.FloatString(18)
}

type discoveredContract struct {
	Address common.Address
	Creator common.Address
	TxHash  common.Hash
	CodeLen int
	Block   uint64
}

func main() {
	logf := initLogger()
	defer logf.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	// Dial WebSocket RPC (for subscriptions)
	rpcClient, err := rpc.Dial(wsURL)
	if err != nil {
		log.Fatalf("❌ Could not connect to Geth at %s: %v", wsURL, err)
	}
	defer rpcClient.Close()

	client := ethclient.NewClient(rpcClient)
	defer client.Close()

	// Quick ping
	_, err = client.ChainID(ctx)
	if err != nil {
		log.Fatalf("❌ RPC health check failed: %v", err)
	}
	log.Printf("✅ Connected to Geth: %s\n", wsURL)

	// Map of discovered contracts
	var mu sync.RWMutex
	contracts := map[common.Address]discoveredContract{}

	// Subscribe to new heads (blocks)
	headers := make(chan *gethtypes.Header, 64)
	headSub, err := client.SubscribeNewHead(ctx, headers)
	if err != nil {
		log.Fatalf("failed to subscribe new heads: %v", err)
	}
	log.Println("🔍 Listening for blocks...")

	// Subscribe to pending transactions (best-effort)
	pendingTxs := make(chan common.Hash, 256)
	pendingSub, err := client.Client().EthSubscribe(ctx, pendingTxs, "newPendingTransactions")
	if err != nil {
		log.Printf("⚠ Pending TX subscribe not available: %v", err)
	} else {
		log.Println("🔍 Listening for pending transactions...")
	}

	// Subscribe to all logs (all contracts). You can filter later if needed.
	logsCh := make(chan gethtypes.Log, 256)
	logsSub, err := client.SubscribeFilterLogs(ctx, ethereum.FilterQuery{}, logsCh)
	if err != nil {
		log.Printf("⚠ Log subscribe not available: %v", err)
	} else {
		log.Println("🔍 Listening for contract events (raw logs)…")
	}

	// Optional: warm scan last N blocks for recent contract creations
	go warmScanRecentBlocks(ctx, client, 50, &mu, contracts)

	// Main event loop
	for {
		select {
		case err := <-headSub.Err():
			log.Printf("⚠ head subscription error: %v", err)
			time.Sleep(2 * time.Second)

		case h := <-headers:
			// Process new block
			if h == nil {
				continue
			}
			processBlock(ctx, client, h.Hash(), &mu, contracts)

		case err := <-pendingSub.Err():
			if err != nil {
				log.Printf("⚠ pending subscription error: %v", err)
				time.Sleep(2 * time.Second)
			}

		case txHash := <-pendingTxs:
			tx, _, err := client.TransactionByHash(ctx, txHash)
			if err != nil {
				log.Printf("Failed to get pending tx %s: %v", txHash.Hex(), err)
				continue
			}
			if tx != nil {
				logPendingTx(ctx, client, tx)
			}

		case err := <-logsSub.Err():
			if err != nil {
				log.Printf("⚠ logs subscription error: %v", err)
				time.Sleep(2 * time.Second)
			}

		case lg := <-logsCh:
			// Raw event log (topics/data). Without ABI we can't decode fields.
			log.Printf("🎉 Log: addr=%s block=%d tx=%s topics=%d dataLen=%d",
				lg.Address.Hex(), lg.BlockNumber, lg.TxHash.Hex(), len(lg.Topics), len(lg.Data))

		case <-sigc:
			log.Println("👋 Shutting down…")
			return
		}
	}
}

func processBlock(ctx context.Context, client *ethclient.Client, blockHash common.Hash, mu *sync.RWMutex, contracts map[common.Address]discoveredContract) {
	block, err := client.BlockByHash(ctx, blockHash)
	if err != nil {
		log.Printf("⚠ get block failed: %v", err)
		return
	}
	log.Printf("📦 New Block: %d | %d txns | hash=%s", block.NumberU64(), len(block.Transactions()), block.Hash().Hex())

	chainID, err := client.ChainID(ctx)
	if err != nil {
		log.Printf("⚠ get chainID failed: %v", err)
	}

	signer := gethtypes.LatestSignerForChainID(chainID)

	for _, tx := range block.Transactions() {
		from := "?"
		if fromAddr, err := gethtypes.Sender(signer, tx); err == nil {
			from = fromAddr.Hex()
		}

		toStr := "<contract-creation>"
		if tx.To() != nil {
			toStr = tx.To().Hex()
		}

		log.Printf("   TX %s | From: %s -> To: %s | Value: %s ETH",
			tx.Hash().Hex(), from, toStr, weiToEther(tx.Value()))

		// Detect contract creation (To() == nil), record details
		if tx.To() == nil {
			rcpt, err := client.TransactionReceipt(ctx, tx.Hash())
			if err != nil {
				// Receipt might not be ready yet in reorg-y conditions
				continue
			}
			addr := rcpt.ContractAddress
			if (addr != common.Address{}) {
				code, err := client.CodeAt(ctx, addr, rcpt.BlockNumber)
				if err != nil {
					log.Printf("     ⚠ get code failed for %s: %v", addr.Hex(), err)
					continue
				}
				var creator common.Address
				if from != "?" {
					creator = common.HexToAddress(from)
				}
				mu.Lock()
				contracts[addr] = discoveredContract{
					Address: addr,
					Creator: creator,
					TxHash:  tx.Hash(),
					CodeLen: len(code),
					Block:   rcpt.BlockNumber.Uint64(),
				}
				mu.Unlock()

				log.Printf("     🆕 Discovered contract: %s | creator=%s | block=%d | codeLen=%d",
					addr.Hex(), creator.Hex(), rcpt.BlockNumber.Uint64(), len(code))
			}
		}
	}
}

func logPendingTx(ctx context.Context, client *ethclient.Client, tx *gethtypes.Transaction) {
	toStr := "nil"
	if tx.To() != nil {
		toStr = tx.To().Hex()
	}
	// We can’t always recover sender for pending (no block base fee). Try latest signer with chainID.
	chainID, err := client.ChainID(ctx)
	var from string
	if err == nil {
		signer := gethtypes.LatestSignerForChainID(chainID)
		if fromAddr, e := gethtypes.Sender(signer, tx); e == nil {
			from = fromAddr.Hex()
		}
	}
	if from == "" {
		from = "?"
	}
	log.Printf("🚀 Pending TX: %s | From: %s -> To: %s | Value: %s ETH",
		tx.Hash().Hex(), from, toStr, weiToEther(tx.Value()))
}

// Optionally look back N blocks at startup for recent contract creations
func warmScanRecentBlocks(ctx context.Context, client *ethclient.Client, lookback uint64, mu *sync.RWMutex, contracts map[common.Address]discoveredContract) {
	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		log.Printf("warm scan: get head err: %v", err)
		return
	}
	start := head.Number.Uint64()
	var begin uint64
	if start > lookback {
		begin = start - lookback
	} else {
		begin = 0
	}

	chainID, err := client.ChainID(ctx)
	if err != nil {
		log.Printf("warm scan: chainID err: %v", err)
		return
	}
	signer := gethtypes.LatestSignerForChainID(chainID)

	log.Printf("🧹 Warm scan last %d blocks (%d..%d) for contracts…", lookback, begin, start)
	for bn := begin; bn <= start; bn++ {
		b, err := client.BlockByNumber(ctx, big.NewInt(int64(bn)))
		if err != nil {
			continue
		}
		for _, tx := range b.Transactions() {
			if tx.To() == nil {
				rcpt, err := client.TransactionReceipt(ctx, tx.Hash())
				if err != nil {
					continue
				}
				addr := rcpt.ContractAddress
				if (addr == common.Address{}) {
					continue
				}
				code, err := client.CodeAt(ctx, addr, rcpt.BlockNumber)
				if err != nil {
					continue
				}
				from := common.Address{}
				if fromAddr, err := gethtypes.Sender(signer, tx); err == nil {
					from = fromAddr
				}
				mu.Lock()
				contracts[addr] = discoveredContract{
					Address: addr,
					Creator: from,
					TxHash:  tx.Hash(),
					CodeLen: len(code),
					Block:   rcpt.BlockNumber.Uint64(),
				}
				mu.Unlock()
				log.Printf("     🔎 Found existing contract: %s | creator=%s | block=%d | codeLen=%d",
					addr.Hex(), from.Hex(), rcpt.BlockNumber.Uint64(), len(code))
			}
		}
	}
}

// --- Optional helpers below (if you later load ABI to decode events) ---

// parseTopicHash is handy if you want to match specific event signatures by keccak(sig)
func parseTopicHash(hexStr string) common.Hash {
	b, _ := hex.DecodeString(strip0x(hexStr))
	return common.BytesToHash(b)
}
func strip0x(s string) string {
	if len(s) >= 2 && s[0:2] == "0x" {
		return s[2:]
	}
	return s
}