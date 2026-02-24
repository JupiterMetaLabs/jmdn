package gETH

// Struct Block - use config.ZKBlock
type Block struct {
	Header          *BlockHeader   `json:"header"`
	Transactions    []*Transaction `json:"transactions"`
	Ommers          [][]byte       `json:"ommers"`
	WithdrawalsRoot []byte         `json:"withdrawalsroot"`
	Withdrawals     []*Withdrawal  `json:"withdrawals"`
	BlobGasUsed     []byte         `json:"blobgasused"`
	ExcessBlobGas   []byte         `json:"excessblobgas"`
}

type BlockHeader struct {
	ParentHash          []byte `json:"parenthash"`
	StateRoot           []byte `json:"stateroot"`
	ReceiptsRoot        []byte `json:"receiptsroot"`
	LogsBloom           []byte `json:"logsbloom"`
	Miner               []byte `json:"miner"`
	Number              uint64 `json:"number"`
	GasLimit            uint64 `json:"gaslimit"`
	GasUsed             uint64 `json:"gasused"`
	Timestamp           uint64 `json:"timestamp"`
	MixHashOrPrevRandao []byte `json:"mixhashorprevrandao"`
	BaseFee             []byte `json:"basefee"`
	BlobGasUsedField    uint64 `json:"blobgasusedfield"`
	ExcessBlobGasField  uint64 `json:"excessblobgasfield"`
	ExtraData           []byte `json:"extradata"`
	Hash                []byte `json:"hash"`
}

// Struct Transaction - use config.Transaction
type Transaction struct {
	Hash                 []byte      `json:"hash"`
	From                 []byte      `json:"from"`
	To                   []byte      `json:"to"`
	Input                []byte      `json:"input"`
	Nonce                uint64      `json:"nonce"`
	Value                []byte      `json:"value"`
	Gas                  uint64      `json:"gas"`
	GasPrice             []byte      `json:"gasprice"`
	Type                 uint32      `json:"type"`
	R                    []byte      `json:"r"`
	S                    []byte      `json:"s"`
	V                    uint32      `json:"v"`
	AccessList           *AccessList `json:"accesslist"`
	MaxFeePerGas         []byte      `json:"maxfeepergas"`
	MaxPriorityFeePerGas []byte      `json:"maxpriorityfeepergas"`
	MaxFeePerBlobGas     []byte      `json:"maxfeeperblobgas"`
	BlobVersionedHashes  [][]byte    `json:"blobversionedhashes"`
}

type AccessList struct {
	AccessTuples []*AccessTuple `json:"accesstuples"`
}

type AccessTuple struct {
	Address     []byte   `json:"address"`
	StorageKeys [][]byte `json:"storagekeys"`
}

type Withdrawal struct {
	Index          uint64 `json:"index"`
	ValidatorIndex uint64 `json:"validatorindex"`
	Address        []byte `json:"address"`
	Amount         uint64 `json:"amount"`
}

// Struct Receipt - use config.Receipt
type Receipt struct {
	TxHash            []byte `json:"txhash"`
	Status            uint64 `json:"status"`
	CumulativeGasUsed uint64 `json:"cumulativegasused"`
	GasUsed           uint64 `json:"gasused"`
	Logs              []*Log `json:"logs"`
	ContractAddress   []byte `json:"contractaddress"`
	Type              uint32 `json:"type"`
	BlockHash         []byte `json:"blockhash"`
	BlockNumber       uint64 `json:"blocknumber"`
	TransactionIndex  uint64 `json:"transactionindex"`
}

// Struct Log - use config.Log
type Log struct {
	Address     []byte   `json:"address"`
	Topics      [][]byte `json:"topics"`
	Data        []byte   `json:"data"`
	BlockNumber uint64   `json:"blocknumber"`
	BlockHash   []byte   `json:"blockhash"`
	TxIndex     uint64   `json:"txindex"`
	TxHash      []byte   `json:"txhash"`
	LogIndex    uint64   `json:"logindex"`
	Removed     bool     `json:"removed"`
}

// Request/Response messages
type GetBlockByNumberReq struct {
	Number uint64 `json:"number"`
	FullTx bool   `json:"fulltx"`
}

type GetBlockByHashReq struct {
	Hash   []byte `json:"hash"`
	FullTx bool   `json:"fulltx"`
}

type GetByHashReq struct {
	Hash []byte `json:"hash"`
}

type GetAccountStateReq struct {
	Address     []byte `json:"address"`
	BlockHash   []byte `json:"blockhash"`
	BlockNumber uint64 `json:"blocknumber"`
}

type AccountState struct {
	Nonce       []byte `json:"nonce"`
	Balance     []byte `json:"balance"`
	StorageRoot []byte `json:"storageroot"`
	CodeHash    []byte `json:"codehash"`
	Code        []byte `json:"code"`
}

type GetLogsReq struct {
	Filter *FilterQuery `json:"filter"`
}

type FilterQuery struct {
	Addresses [][]byte `json:"addresses"`
	Topics    [][]byte `json:"topics"`
	BlockHash []byte   `json:"blockhash"`
	FromBlock uint64   `json:"fromblock"`
	ToBlock   uint64   `json:"toblock"`
}

type GetLogsResp struct {
	Logs []*Log `json:"logs"`
}

type CallReq struct {
	From                 []byte `json:"from"`
	To                   []byte `json:"to"`
	Gas                  []byte `json:"gas"`
	GasPrice             []byte `json:"gasprice"`
	MaxFeePerGas         []byte `json:"maxfeepergas"`
	MaxPriorityFeePerGas []byte `json:"maxpriorityfeepergas"`
	Value                []byte `json:"value"`
	Data                 []byte `json:"data"`
	Nonce                uint64 `json:"nonce"`
}

type CallResp struct {
	ReturnData []byte `json:"returndata"`
	GasUsed    uint64 `json:"gasused"`
	Error      string `json:"error"`
}

type EstimateResp struct {
	GasEstimate uint64 `json:"gasestimate"`
	Error       string `json:"error"`
}

type SendRawTxReq struct {
	SignedTx []byte `json:"signedtx"`
}

type SendRawTxResp struct {
	TxHash []byte `json:"txhash"`
	Error  string `json:"error"`
}

type LogsSubReq struct {
	Addresses [][]byte `json:"addresses"`
	Topics    [][]byte `json:"topics"`
}
