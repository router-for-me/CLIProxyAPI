package localrouting

import (
	"fmt"
	"net"
)

func FindFreePort(minPort, maxPort int) (int, error) {
	if minPort <= 0 {
		minPort = MinAppPort
	}
	if maxPort <= 0 {
		maxPort = MaxAppPort
	}
	if minPort > maxPort {
		return 0, fmt.Errorf("invalid port range: %d-%d", minPort, maxPort)
	}
	for port := minPort; port <= maxPort; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return port, nil
	}
	return 0, fmt.Errorf("no available port in range %d-%d", minPort, maxPort)
}
