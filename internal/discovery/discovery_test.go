package discovery

import (
	"strings"
	"testing"
)

func TestLanSkipsContainerAndVPNInterfaces(t *testing.T) {
	for name, want := range map[string]bool{"eth0": true, "en0": true, "wlan0": true, "enp3s0": true,
		"docker0": false, "br-1a2b": false, "veth12": false, "utun3": false, "awdl0": false, "cni0": false} {
		if lan(name) != want {
			t.Errorf("lan(%q) = %v", name, !want)
		}
	}
}

func TestHostNameIsOneLabel(t *testing.T) {
	if h := hostName(); h == "" || strings.Contains(h, ".") {
		t.Fatalf("hostName() = %q", h)
	}
}
