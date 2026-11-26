// Package discovery announces the server on the local network (mDNS / DNS-SD),
// so Sepia TV apps find it without an address typed in.
package discovery

import (
	"errors"
	"net"
	"os"
	"strings"

	"github.com/libp2p/zeroconf/v2"
)

// Service is the DNS-SD type apps browse for. Apps ship with it (iOS lists it
// in Info.plist), so it never changes.
const Service = "_sepia-server._tcp"

// virtual are interfaces of containers, VPNs and Apple's peer links: their
// addresses are of no use to an app on the home network.
var virtual = []string{"docker", "br-", "veth", "cni", "flannel", "cali", "virbr", "tun", "utun", "awdl", "llw", "anpi", "bridge"}

func lan(name string) bool {
	for _, p := range virtual {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// addrs lists the multicast interfaces of the home network and their addresses.
func addrs() (ifaces []net.Interface, ips []string) {
	all, _ := net.Interfaces()
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 || !lan(ifi.Name) {
			continue
		}
		as, _ := ifi.Addrs()
		n := len(ips)
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && (ipn.IP.To4() != nil || ipn.IP.IsGlobalUnicast()) {
				ips = append(ips, ipn.IP.String())
			}
		}
		if len(ips) > n {
			ifaces = append(ifaces, ifi)
		}
	}
	return ifaces, ips
}

// hostName is the machine's name as one label: zeroconf adds ".local." to
// whatever it gets, and macOS already reports "name.local".
func hostName() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	if h == "" {
		h = "sepia-server"
	}
	return h
}

// Announce advertises the server until the returned stop is called. It only
// reaches the network the process is on: in a container that is the host's
// network only with host networking.
func Announce(plexName, machineIdentifier, version string, port int) (stop func(), err error) {
	name := "Sepia Server"
	if plexName != "" {
		name += " (" + plexName + ")"
	}
	ifaces, ips := addrs()
	if len(ips) == 0 {
		return nil, errors.New("no network interface to announce on")
	}
	srv, err := zeroconf.RegisterProxy(name, Service, "local.", port, hostName(), ips,
		[]string{"machineIdentifier=" + machineIdentifier, "version=" + version}, ifaces)
	if err != nil {
		return nil, err
	}
	return srv.Shutdown, nil
}
