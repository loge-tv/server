// Package discovery announces the server on the local network (mDNS / DNS-SD),
// so Sepia TV apps find it without an address typed in.
package discovery

import (
	"github.com/libp2p/zeroconf/v2"
)

// Service is the DNS-SD type apps browse for. Apps ship with it (iOS lists it
// in Info.plist), so it never changes.
const Service = "_sepia-server._tcp"

// Announce advertises the server until the returned stop is called. It only
// reaches the network the process is on: in a container that is the host's
// network only with host networking.
func Announce(plexName, machineIdentifier, version string, port int) (stop func(), err error) {
	name := "Sepia Server"
	if plexName != "" {
		name += " (" + plexName + ")"
	}
	srv, err := zeroconf.Register(name, Service, "local.", port,
		[]string{"machineIdentifier=" + machineIdentifier, "version=" + version}, nil)
	if err != nil {
		return nil, err
	}
	return srv.Shutdown, nil
}
