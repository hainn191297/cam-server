package onvif

import (
	"fmt"
	"net"
)

// listNICNames returns the names of all non-loopback network interfaces on
// the current host. Used by discoverMulticast to probe all candidates.
func listNICNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	names := make([]string, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue // skip loopback
		}
		if iface.Flags&net.FlagUp == 0 {
			continue // skip down interfaces
		}
		names = append(names, iface.Name)
	}
	return names, nil
}
