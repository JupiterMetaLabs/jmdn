package settings

import (
	"net"
	"strconv"
	"strings"
)

// ListenAddr returns the host:port a service listens on: the configured bind
// address for that service (selected by pick from the loaded binds section),
// or 127.0.0.1 when settings are not loaded or the bind is empty. Services must
// listen on this rather than ":port", which binds every interface regardless
// of configuration.
func ListenAddr(pick func(BindSettings) string, port int) string {
	host := ""
	if IsLoaded() && pick != nil {
		host = strings.TrimSpace(pick(Get().Binds))
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
