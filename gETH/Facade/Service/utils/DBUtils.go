package Utils

import (
	"gossipnode/DB_OPs"
	"gossipnode/messaging"
	"gossipnode/node"
)

func CreateAccountandPropagateDID(Document DIDDoc) error {
	// GATED out-of-band creation: accounts created here exist on some nodes only,
	// with a locally minted ART nonce — the fleet-divergence vector removed by
	// block-carried identities. Disabled unless JMDN_ALLOW_LOCAL_ACCOUNT_CREATE=1.
	if !DB_OPs.AllowLocalAccountCreate {
		return DB_OPs.ErrLocalAccountCreateDisabled
	}

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
