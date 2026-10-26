package gateway

import (
	"net"
	"strconv"
	"strings"
)

// joinHostPort builds the host:port a gateway dials from a configured host
// and port. net.JoinHostPort puts an IPv6 literal in brackets; a host that
// already has them is taken without, so "[::1]" keeps working as it did
// when the address was built with "%s:%d". [MESHSAT-1676]
func joinHostPort(host string, port int) string {
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}
