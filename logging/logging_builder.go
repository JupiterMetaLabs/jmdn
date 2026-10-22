package logging

import (
	"context"
	"fmt"
	"time"

	"github.com/JupiterMetaLabs/ion"
)

func (lb *Logging) GetNamedLogger() *ion.Ion {
	return lb.NamedLogger
}

func (lb *Logging) Close() error {
	if lb.NamedLogger == nil {
		return fmt.Errorf("NamedLogger is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return lb.NamedLogger.Shutdown(ctx)
}
