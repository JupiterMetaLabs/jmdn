package Utils

import (
	"gossipnode/DB_OPs"
	"gossipnode/messaging"
	"gossipnode/node"
)

func CreateAccountandPropagateDID(Document DIDDoc) error {

	// Create the account
	err := DB_OPs.CreateAccount(nil, Document.DIDAddress, Document.Address, Document.Metadata)
	if err != nil {
		return err
	}

	// Get the account from the DB
	account, err := DB_OPs.GetAccount(nil, Document.Address)
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