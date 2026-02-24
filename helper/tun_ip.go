package helper

import (
	"fmt"
	"net"
)

// GetTun0GlobalIPv6 retrieves the global IPv6 address for Yggdrasil interface
// Tries multiple common Yggdrasil interface names
func GetTun0GlobalIPv6() (string, error) {
	// Common Yggdrasil interface names to try
	interfaceNames := []string{"utun6", "tun0", "utun0", "ygg0", "yggdrasil"}

	for _, ifaceName := range interfaceNames {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			continue // Try next interface name
		}

		// Get all interface addresses
		addrs, err := iface.Addrs()
		if err != nil {
			continue // Try next interface name
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
				fmt.Printf("Found Yggdrasil IPv6 address %s on interface %s\n", ipNet.IP.String(), ifaceName)
				return ipNet.IP.String(), nil
			}
		}
	}

	// If none of the common interface names worked, try scanning all interfaces
	fmt.Printf("Common interface names failed, scanning all interfaces for Yggdrasil addresses...\n")
	return scanAllInterfacesForYggdrasil()
}

// scanAllInterfacesForYggdrasil scans all network interfaces to find Yggdrasil IPv6 addresses
func scanAllInterfacesForYggdrasil() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("could not get network interfaces: %w", err)
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Check if it's a global IPv6 address
			if ipNet.IP.To16() != nil && !ipNet.IP.IsLoopback() && !ipNet.IP.IsLinkLocalUnicast() {
				// Check if this looks like a Yggdrasil address (starts with 200: or 203:)
				ipStr := ipNet.IP.String()
				if len(ipStr) >= 4 && (ipStr[:4] == "200:" || ipStr[:4] == "203:") {
					fmt.Printf("Found potential Yggdrasil IPv6 address %s on interface %s\n", ipStr, iface.Name)
					return ipStr, nil
				}
			}
		}
	}

	return "", fmt.Errorf("no Yggdrasil IPv6 address found on any interface")
}
