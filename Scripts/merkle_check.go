// scripts/block_merkle/main.go
//
// Fetches all ZKBlocks from ImmuDB, computes a single hash per block
// (from all fields EXCEPT BlockHash), then builds ONE Merkle tree where:
//
//	Level 0  — Leaves: one per block  (leaf.hash = SHA256 of all block fields)
//	Level 1+ — Parents: SHA256(left_child || right_child)
//	Top      — Root: the final single hash
//
// Outputs ONE JSON file:
//
//	{
//	  "root":          "<hex>",
//	  "total_blocks":  732,
//	  "from_block":    1,
//	  "to_block":      732,
//	  "generated_at":  "2026-04-22T06:00:00Z",
//	  "leaves": [
//	    { "block_number": 1, "block_hash_excluded": true, "hash": "<hex>" },
//	    ...
//	  ],
//	  "levels": [
//	    { "level": 0, "label": "Leaves",  "nodes": [...] },
//	    { "level": 1, "label": "Level 1", "nodes": [...] },
//	    { "level": N, "label": "Root",    "nodes": [{ "hash": "<root>" }] }
//	  ]
//	}
//
// Usage:
//
//	go run ./scripts/block_merkle/main.go \
//	    -out merkle_all.json \
//	    -user immudb -pass immudb
//
//	# specific range
//	go run ./scripts/block_merkle/main.go \
//	    -from 1 -to 500 \
//	    -out merkle_1_500.json \
//	    -user immudb -pass immudb
//
// Flags:
//
//	-from     N    First block (default: 1)
//	-to       N    Last block  (default: latest in DB)
//	-out      s    Output JSON file (default: merkle_all.json)
//	-workers  N    Concurrent fetch workers (default: 4)
//	-user     s    ImmuDB username
//	-pass     s    ImmuDB password
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"gossipnode/DB_OPs"
	"gossipnode/config"
	"gossipnode/config/settings"
	"gossipnode/logging"
)

// ---------------------------------------------------------------------------
// Output types — one combined file for the whole chain
// ---------------------------------------------------------------------------

// BlockLeaf is one entry in the leaf level of the global Merkle tree.
type BlockLeaf struct {
	BlockNumber       uint64 `json:"block_number"`
	BlockHashExcluded bool   `json:"block_hash_excluded"` // always true — BlockHash is never hashed
	Hash              string `json:"hash"`                // SHA256 over all other block fields
}

// Node is one hash in any Merkle level.
// Left/Right are indices into the level directly below (-1 for leaves).
//
// FromBlock/ToBlock record the inclusive range of block numbers this node
// covers. At leaf level they are both equal to the single block number.
// At every parent level they span left-child.FromBlock → right-child.ToBlock.
// A divergence algorithm can use these to narrow the search without walking
// all the way down to the leaves: if hashes differ, follow the child whose
// [FromBlock, ToBlock] contains the expected divergence range.
type Node struct {
	Index      int    `json:"index"`
	Hash       string `json:"hash"`
	FromBlock  uint64 `json:"from_block"`           // first block covered by this subtree
	ToBlock    uint64 `json:"to_block"`             // last block covered by this subtree
	Left       int    `json:"left"`                 // child index in level below (-1 for leaves)
	Right      int    `json:"right"`                // child index in level below (-1 for leaves)
	Duplicated bool   `json:"duplicated,omitempty"` // true when padded from an odd count
}

// Level is one horizontal row of the tree.
type Level struct {
	Level int    `json:"level"` // 0 = leaves, highest = root
	Label string `json:"label"`
	Nodes []Node `json:"nodes"`
}

// MerkleForest is the single output JSON.
type MerkleForest struct {
	Root        string      `json:"root"`
	TotalBlocks uint64      `json:"total_blocks"`
	FromBlock   uint64      `json:"from_block"`
	ToBlock     uint64      `json:"to_block"`
	GeneratedAt string      `json:"generated_at"`
	ErrorCount  int         `json:"error_count,omitempty"`
	Leaves      []BlockLeaf `json:"leaves"`
	Levels      []Level     `json:"levels"`
}

// ---------------------------------------------------------------------------
// Block hashing — one canonical hash per block (BlockHash excluded)
//
// Excluded fields (DB/derived, not part of canonical block content):
//   - BlockHash: derived from the other fields; including it would be circular
// ---------------------------------------------------------------------------

func hashBlock(b *config.ZKBlock) string {
	h := sha256.New()

	buf8 := make([]byte, 8)
	buf4len := make([]byte, 4)

	// writeVar writes a 4-byte big-endian length prefix followed by the data.
	// This prevents boundary-shift collisions between adjacent variable-length
	// fields, e.g. ("AB"+"CD") vs ("A"+"BCD") producing the same byte stream.
	writeVar := func(dst interface{ Write([]byte) (int, error) }, data []byte) {
		binary.BigEndian.PutUint32(buf4len, uint32(len(data)))
		dst.Write(buf4len)
		dst.Write(data)
	}

	// ── Scalars ───────────────────────────────────────────────────────────
	binary.BigEndian.PutUint64(buf8, b.BlockNumber)
	h.Write(buf8)

	binary.BigEndian.PutUint64(buf8, uint64(b.Timestamp))
	h.Write(buf8)

	binary.BigEndian.PutUint64(buf8, b.GasLimit)
	h.Write(buf8)

	binary.BigEndian.PutUint64(buf8, b.GasUsed)
	h.Write(buf8)

	// ── Fixed-size hashes (always 32 bytes — no length prefix needed) ─────
	h.Write(b.PrevHash.Bytes())
	h.Write(b.StateRoot.Bytes())

	// ── Strings (variable length — length-prefixed) ────────────────────────
	writeVar(h, []byte(b.TxnsRoot))
	writeVar(h, []byte(b.ProofHash))
	writeVar(h, []byte(b.Status))
	writeVar(h, []byte(b.ExtraData))

	// ── Byte slices (variable length — length-prefixed) ───────────────────
	writeVar(h, b.StarkProof)
	writeVar(h, b.LogsBloom)

	// ── Commitment ([]uint32) ─────────────────────────────────────────────
	buf4 := make([]byte, 4)
	for _, v := range b.Commitment {
		binary.BigEndian.PutUint32(buf4, v)
		h.Write(buf4)
	}

	// ── Nullable addresses ────────────────────────────────────────────────
	if b.CoinbaseAddr != nil {
		h.Write(b.CoinbaseAddr.Bytes())
	} else {
		h.Write([]byte("nil"))
	}
	if b.ZKVMAddr != nil {
		h.Write(b.ZKVMAddr.Bytes())
	} else {
		h.Write([]byte("nil"))
	}

	// ── Transactions ──────────────────────────────────────────────────────
	for _, tx := range b.Transactions {
		th := sha256.New()

		th.Write(tx.Hash.Bytes())

		if tx.From != nil {
			th.Write(tx.From.Bytes())
		}
		if tx.To != nil {
			th.Write(tx.To.Bytes())
		}
		if tx.Value != nil {
			th.Write(tx.Value.Bytes())
		}

		th.Write([]byte{tx.Type})

		binary.BigEndian.PutUint64(buf8, tx.Timestamp)
		th.Write(buf8)

		binary.BigEndian.PutUint64(buf8, tx.Nonce)
		th.Write(buf8)

		binary.BigEndian.PutUint64(buf8, tx.GasLimit)
		th.Write(buf8)

		if tx.ChainID != nil {
			th.Write(tx.ChainID.Bytes())
		}
		if tx.GasPrice != nil {
			th.Write(tx.GasPrice.Bytes())
		}
		if tx.MaxFee != nil {
			th.Write(tx.MaxFee.Bytes())
		}
		if tx.MaxPriorityFee != nil {
			th.Write(tx.MaxPriorityFee.Bytes())
		}

		writeVar(th, tx.Data)

		// AccessList: each entry = address + storage keys
		for _, entry := range tx.AccessList {
			th.Write(entry.Address.Bytes())
			for _, key := range entry.StorageKeys {
				th.Write(key.Bytes())
			}
		}

		if tx.V != nil {
			th.Write(tx.V.Bytes())
		}
		if tx.R != nil {
			th.Write(tx.R.Bytes())
		}
		if tx.S != nil {
			th.Write(tx.S.Bytes())
		}

		h.Write(th.Sum(nil))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Global Merkle tree builder
// ---------------------------------------------------------------------------

func buildMerkleTree(leaves []BlockLeaf) (levels []Level, root string) {
	if len(leaves) == 0 {
		// SHA256 of the empty string — not a zero hash
		empty := hex.EncodeToString(sha256.New().Sum(nil))
		return []Level{{Level: 0, Label: "Root", Nodes: []Node{{Hash: empty, Left: -1, Right: -1}}}}, empty
	}

	// Level 0: one node per leaf — FromBlock == ToBlock == the block's own number.
	leafNodes := make([]Node, len(leaves))
	for i, l := range leaves {
		leafNodes[i] = Node{
			Index:     i,
			Hash:      l.Hash,
			FromBlock: l.BlockNumber,
			ToBlock:   l.BlockNumber,
			Left:      -1,
			Right:     -1,
		}
	}
	levels = append(levels, Level{Level: 0, Label: "Leaves", Nodes: leafNodes})

	cur := make([]string, len(leaves))
	for i, l := range leaves {
		cur[i] = l.Hash
	}

	for lvl := 1; len(cur) > 1; lvl++ {
		padded := false
		if len(cur)%2 != 0 {
			cur = append(cur, cur[len(cur)-1]) // duplicate last
			padded = true
		}

		prevLevel := levels[lvl-1]
		prevCount := len(prevLevel.Nodes) // real node count before padding
		next := make([]string, len(cur)/2)
		nodes := make([]Node, len(cur)/2)

		for i := 0; i < len(cur); i += 2 {
			l, _ := hex.DecodeString(cur[i])
			r, _ := hex.DecodeString(cur[i+1])
			h := sha256.Sum256(append(l, r...))
			next[i/2] = hex.EncodeToString(h[:])

			li, ri := i, i+1
			isDup := padded && ri >= prevCount
			if li >= prevCount {
				li = prevCount - 1
			}
			if ri >= prevCount {
				ri = prevCount - 1
			}

			// Parent covers left-child.From → right-child.To.
			nodes[i/2] = Node{
				Index:      i / 2,
				Hash:       next[i/2],
				FromBlock:  prevLevel.Nodes[li].FromBlock,
				ToBlock:    prevLevel.Nodes[ri].ToBlock,
				Left:       li,
				Right:      ri,
				Duplicated: isDup,
			}
		}

		label := fmt.Sprintf("Level %d", lvl)
		if len(next) == 1 {
			label = "Root"
		}
		levels = append(levels, Level{Level: lvl, Label: label, Nodes: nodes})
		cur = next
	}

	return levels, cur[0]
}

// ---------------------------------------------------------------------------
// Worker result
// ---------------------------------------------------------------------------

type result struct {
	blockNum uint64
	hash     string
	err      error
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	fromFlag := flag.Uint64("from", 0, "First block number (default: 1)")
	toFlag := flag.Uint64("to", 0, "Last block number (default: latest in DB)")
	outFile := flag.String("out", "merkle_all.json", "Output JSON file")
	numWorkers := flag.Int("workers", 4, "Concurrent fetch workers")
	user := flag.String("user", "", "ImmuDB username")
	pass := flag.String("pass", "", "ImmuDB password")
	flag.Parse()

	// ── 1. Load settings ──────────────────────────────────────────────────
	cfg, err := settings.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: load settings: %v\n", err)
		os.Exit(1)
	}
	username := cfg.Database.Username
	password := cfg.Database.Password
	if *user != "" {
		username = *user
	}
	if *pass != "" {
		password = *pass
	}

	// ── 2. Bootstrap logger ───────────────────────────────────────────────
	logging.NewAsyncLogger()

	// ── 3. Init DB pool ───────────────────────────────────────────────────
	poolCfg := config.DefaultConnectionPoolConfig()
	if err := DB_OPs.InitMainDBPoolWithLoki(poolCfg, false, username, password); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: init DB pool: %v\n", err)
		os.Exit(1)
	}
	defer DB_OPs.CloseMainDBPool()

	// ── 4. Resolve range ──────────────────────────────────────────────────
	fromBlock := *fromFlag
	if fromBlock == 0 {
		fromBlock = 1
	}

	toBlock := *toFlag
	if toBlock == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
		if err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "ERROR: get connection: %v\n", err)
			os.Exit(1)
		}
		latest, err := DB_OPs.GetLatestBlockNumber(conn)
		cancel() // cancel AFTER use so GRO returns the connection at the right time
		if err != nil || latest == 0 {
			fmt.Fprintf(os.Stderr, "ERROR: get latest block: %v\n", err)
			os.Exit(1)
		}
		toBlock = latest
	}

	if fromBlock > toBlock {
		fmt.Fprintf(os.Stderr, "ERROR: -from (%d) > -to (%d)\n", fromBlock, toBlock)
		os.Exit(1)
	}

	total := toBlock - fromBlock + 1
	fmt.Printf("Fetching blocks %d → %d  (%d blocks, %d workers)\n", fromBlock, toBlock, total, *numWorkers)

	// ── 5. Concurrent fetch ───────────────────────────────────────────────
	jobs := make(chan uint64, *numWorkers*2)
	results := make(chan result, *numWorkers*2)
	var wg sync.WaitGroup

	for w := 0; w < *numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for num := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				conn, err := DB_OPs.GetMainDBConnectionandPutBack(ctx)
				if err != nil {
					cancel()
					results <- result{blockNum: num, err: err}
					continue
				}
				block, err := DB_OPs.GetZKBlockByNumber(conn, num)
				cancel() // cancel AFTER use
				if err != nil {
					results <- result{blockNum: num, err: err}
					continue
				}
				if block == nil {
					results <- result{blockNum: num, err: fmt.Errorf("not found")}
					continue
				}
				results <- result{blockNum: num, hash: hashBlock(block)}
			}
		}()
	}

	go func() {
		for n := fromBlock; n <= toBlock; n++ {
			jobs <- n
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	// ── 6. Collect, report progress ───────────────────────────────────────
	type blockResult struct {
		hash string
		err  error
	}
	collected := make(map[uint64]blockResult, int(total))
	done, errCount := 0, 0

	for r := range results {
		done++
		if r.err != nil {
			errCount++
			collected[r.blockNum] = blockResult{err: r.err}
			fmt.Printf("  [%d/%d] block %-8d ERROR: %v\n", done, total, r.blockNum, r.err)
		} else {
			collected[r.blockNum] = blockResult{hash: r.hash}
			if done%100 == 0 || done == int(total) || int(total) <= 100 {
				fmt.Printf("  [%d/%d] block %-8d  hash=%s...\n", done, total, r.blockNum, r.hash[:16])
			}
		}
	}

	// ── 7. Build ordered leaf list (skip errored blocks) ──────────────────
	nums := make([]uint64, 0, len(collected))
	for n := range collected {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })

	leaves := make([]BlockLeaf, 0, len(nums))
	for _, n := range nums {
		r := collected[n]
		if r.err != nil {
			continue
		}
		leaves = append(leaves, BlockLeaf{
			BlockNumber:       n,
			BlockHashExcluded: true,
			Hash:              r.hash,
		})
	}

	fmt.Printf("\nBuilding Merkle tree over %d leaves...\n", len(leaves))

	// ── 8. Build global Merkle tree ───────────────────────────────────────
	levels, root := buildMerkleTree(leaves)

	// ── 9. Write single output JSON ───────────────────────────────────────
	// from_block/to_block reflect the actually-covered range (errors skipped).
	actualFrom, actualTo := fromBlock, toBlock
	if len(leaves) > 0 {
		actualFrom = leaves[0].BlockNumber
		actualTo = leaves[len(leaves)-1].BlockNumber
	}

	out := MerkleForest{
		Root:        root,
		TotalBlocks: uint64(len(leaves)),
		FromBlock:   actualFrom,
		ToBlock:     actualTo,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		ErrorCount:  errCount,
		Leaves:      leaves,
		Levels:      levels,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: marshal output: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outFile, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: write %s: %v\n", *outFile, err)
		os.Exit(1)
	}

	fmt.Printf("\n──────────────────────────────────────────────────────────\n")
	fmt.Printf("Merkle root   : %s\n", root)
	fmt.Printf("Total leaves  : %d  (blocks fetched successfully)\n", len(leaves))
	fmt.Printf("Tree levels   : %d  (leaf level + %d parent levels)\n", len(levels), len(levels)-1)
	fmt.Printf("Errors        : %d\n", errCount)
	fmt.Printf("Output        : %s\n", *outFile)
}
