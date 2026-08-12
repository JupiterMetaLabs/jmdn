package Block

import (
	"context"

	"github.com/JupiterMetaLabs/ion"
)

// LogTransaction logs transaction data in structured format
func LogTransaction(hash, from, to, value, txType string) {
	logger().Info(context.Background(), "Transaction processed",
		ion.String("transaction_hash", hash),
		ion.String("from", from),
		ion.String("to", to),
		ion.String("value", value),
		ion.String("type", txType))
}
