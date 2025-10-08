package Service

import (
	"context"
	"math/big"
	"net/http"
	"go.uber.org/zap"
)

const(
	
)

func LogData(ctx context.Context, Message string, Function string, status int){
	tempLogger := GetLogger()
	if status == http.StatusOK {
	tempLogger.Logger.Info(Message,
		zap.String(logging.Function, Function),
		zap.String(logging.Message, Message),
		zap.String(logging.Status, "OK"),
	)
	} else {
		tempLogger.Logger.Error(Message,
			zap.String(logging.Function, Function),
			zap.String(logging.Message, Message),
			zap.String(logging.Status, status),
		)
	}

	return nil

}


func ChainID(ctx context.Context) (*big.Int, error) {
	ChainID := 7000700
	return big.NewInt(int64(ChainID)), nil
}

func ClientVersion(ctx context.Context) (string, error) {
	ClientVersion := "JMDT/v1.0.0"
	return ClientVersion, nil
}

func BlockNumber(ctx context.Context) (*big.Int, error){

}