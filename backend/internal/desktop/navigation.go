package desktop

import (
	"fmt"
	"net"
	"strconv"
)

func NavigationURL(addr net.Addr) (string, error) {
	if addr == nil {
		return "", fmt.Errorf("listener address is unavailable")
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", fmt.Errorf("invalid listener address: %w", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid listener port")
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
