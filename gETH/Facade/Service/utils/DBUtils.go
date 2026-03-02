package Utils

import (
	"context"
	"time"

	"jmdn/DB_OPs"
	"jmdn/messaging"
	"jmdn/node"
)

func CreateAccountandPropagateDID(Document DIDDoc) error {

	opCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Pull one connection from the db
	PooledConnection, err := DB_OPs.GetAccountConnectionandPutBack(opCtx)
	if err != nil {
		return err
	}
	defer DB_OPs.PutAccountsConnection(PooledConnection)

	// Create the account
	err = DB_OPs.CreateAccount(PooledConnection, Document.DIDAddress, Document.Address, Document.Metadata)
	if err != nil {
		return err
	}

	// Get the account from the DB
	account, err := DB_OPs.GetAccount(PooledConnection, Document.Address)
	if err != nil {
		return err
	}

	// Get the host from the node
	host := node.GetHost()

	// Propagate the DID
	err = messaging.PropagateDID(host, account)
	if err != nil {
		return err
	}

	return nil
}
