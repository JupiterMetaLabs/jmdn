package explorer


import (
	"gossipnode/DB_OPs"
	"gossipnode/config"
)

func GetLatesBlockNumber(DBclient *ImmuDBServer) (uint64, error) {
	latestBlockNumber, err := DB_OPs.GetLatestBlockNumber(&DBclient.defaultdb)
	if err != nil {
		return 0, err
	}
	return latestBlockNumber, nil
}

func GetLatestBlockByNumber(DBclient *ImmuDBServer, blockNumber uint64) (*config.ZKBlock, error) {
	block, err := DB_OPs.GetZKBlockByNumber(&DBclient.defaultdb, blockNumber)
	if err != nil {
		return nil, err
	}
	return block, nil
}