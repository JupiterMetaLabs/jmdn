package helper

import ( 
	"fmt"
	"net"
)

// GetTun0GlobalIPv6 retrieves the global IPv6 address for the tun0 interface
func GetTun0GlobalIPv6() (string, error) {
	// Find the network interface
	iface, err := net.InterfaceByName("tun0")
	if err != nil {
		return "", fmt.Errorf("could not find tun0 interface: %w", err)
	}

	// Get all interface addresses
	addrs, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("could not get interface addresses: %w", err)
	}

	// Iterate through addresses to find global IPv6 address
	for _, addr := range addrs {
		// Check if the address is an IPv6 address
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		// Check if it's a global IPv6 address (first few characters indicate global scope)
		if ipNet.IP.To16() != nil && !ipNet.IP.IsLoopback() && !ipNet.IP.IsLinkLocalUnicast() {
			return ipNet.IP.String(), nil
		}
	}

	return "", fmt.Errorf("no global IPv6 address found for tun0")
}

