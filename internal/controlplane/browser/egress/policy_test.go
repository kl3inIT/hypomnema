package egress_test

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/browser/egress"
)

const controlPlanePort = 18990

func addresses(texts ...string) []netip.Addr {
	out := make([]netip.Addr, len(texts))
	for i, text := range texts {
		out[i] = netip.MustParseAddr(text)
	}

	return out
}

func rules(t *testing.T, allow ...string) egress.Rules {
	t.Helper()

	list, err := egress.ParseAllow(allow)
	require.NoError(t, err)

	return egress.Rules{Allow: list, OwnPort: func(port uint16) bool { return port == controlPlanePort }}
}

// The public internet passes; this machine, its networks and the addresses
// that are no host on the internet do not, however they are written.
func TestRefusalWithNothingAllowed(t *testing.T) {
	// The machine holds a private address and a public one.
	own := map[netip.Addr]bool{
		netip.MustParseAddr("192.168.1.10"): true,
		netip.MustParseAddr("2001:db8::5"):  true,
	}

	for _, tc := range []struct {
		name      string
		port      uint16
		addresses []string
		want      egress.Refusal
	}{
		{"a public IPv4 address", 443, []string{"93.184.215.14"}, egress.Allowed},
		{"a public IPv6 address", 443, []string{"2606:4700::6810:84e5"}, egress.Allowed},
		{"loopback", 8080, []string{"127.0.0.1"}, egress.ThisComputer},
		{"loopback, IPv6", 8080, []string{"::1"}, egress.ThisComputer},
		{"loopback as an IPv4-mapped address", 8080, []string{"::ffff:127.0.0.1"}, egress.ThisComputer},
		{"loopback inside a NAT64 address", 8080, []string{"64:ff9b::7f00:1"}, egress.ThisComputer},
		{"loopback with a zone", 8080, []string{"::1%lo"}, egress.ThisComputer},
		{"the control plane's port", controlPlanePort, []string{"127.0.0.1"}, egress.OwnService},
		{"the machine's private address", 5432, []string{"192.168.1.10"}, egress.ThisComputer},
		{"the machine's public address", 5432, []string{"2001:db8::5"}, egress.ThisComputer},
		{"the control plane's port on the machine's address", controlPlanePort, []string{"2001:db8::5"},
			egress.OwnService},
		{"a private network", 80, []string{"10.0.0.9"}, egress.LocalNetwork},
		{"a neighbor on the machine's network", 80, []string{"192.168.1.11"}, egress.LocalNetwork},
		{"the carrier range Tailscale uses", 80, []string{"100.64.0.7"}, egress.LocalNetwork},
		{"a unique local IPv6 address", 80, []string{"fd12::1"}, egress.LocalNetwork},
		{"a private address inside a 6to4 address", 80, []string{"2002:a00:9::"}, egress.LocalNetwork},
		{"cloud metadata", 80, []string{"169.254.169.254"}, egress.Metadata},
		{"cloud metadata, IPv6", 80, []string{"fd00:ec2::254"}, egress.Metadata},
		{"Alibaba's metadata", 80, []string{"100.100.100.200"}, egress.Metadata},
		{"link-local", 80, []string{"169.254.10.1"}, egress.LinkLocal},
		{"link-local, IPv6", 80, []string{"fe80::1"}, egress.LinkLocal},
		{"the unspecified address", 80, []string{"0.0.0.0"}, egress.Reserved},
		{"the unspecified address, IPv6", 80, []string{"::"}, egress.Reserved},
		{"multicast", 80, []string{"224.0.0.1"}, egress.Reserved},
		{"the benchmark range", 80, []string{"198.18.0.1"}, egress.Reserved},
		{"a name with a public and a loopback address", 443, []string{"93.184.215.14", "127.0.0.1"},
			egress.ThisComputer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rules(t).Refusal(tc.port, addresses(tc.addresses...), own))
		})
	}
}

// The owner's list opens a private destination, by address, range or port.
// It never opens the control plane's own port, metadata or a link-local
// address.
func TestRefusalWithAnAllowList(t *testing.T) {
	listed := rules(t, "10.0.0.5", " 172.16.0.0/12 ", "10.1.0.9:8080", "127.0.0.1:3000", "[fd12::1]:443", "fd13::/48",
		"169.254.0.0/16", "")

	for _, tc := range []struct {
		name    string
		port    uint16
		address string
		want    egress.Refusal
	}{
		{"an address, any port", 9200, "10.0.0.5", egress.Allowed},
		{"its neighbor", 9200, "10.0.0.6", egress.LocalNetwork},
		{"an address in a range", 80, "172.20.1.1", egress.Allowed},
		{"an address and its port", 8080, "10.1.0.9", egress.Allowed},
		{"that address, another port", 8081, "10.1.0.9", egress.LocalNetwork},
		{"a loopback port", 3000, "127.0.0.1", egress.Allowed},
		{"the same port as localhost's IPv6 address", 3000, "::1", egress.Allowed},
		{"another loopback port", 3001, "127.0.0.1", egress.ThisComputer},
		{"a bracketed IPv6 address and its port", 443, "fd12::1", egress.Allowed},
		{"that IPv6 address, another port", 80, "fd12::1", egress.LocalNetwork},
		{"an IPv6 range", 80, "fd13::9", egress.Allowed},
		{"metadata in a listed range", 80, "169.254.169.254", egress.Metadata},
		{"a link-local address in a listed range", 80, "169.254.10.1", egress.LinkLocal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, listed.Refusal(tc.port, addresses(tc.address), nil))
		})
	}

	wholeLoopback := rules(t, "127.0.0.0/8")
	assert.Equal(t, egress.Allowed, wholeLoopback.Refusal(5432, addresses("127.0.0.1"), nil))
	assert.Equal(t, egress.OwnService, wholeLoopback.Refusal(controlPlanePort, addresses("127.0.0.1"), nil),
		"the control plane's port stays shut when all of loopback is listed")
}

func TestParseAllowRefusesWhatIsNotAnAddress(t *testing.T) {
	for _, entry := range []string{
		"example.com", "10.0.0.5/40", "fd12::/129", "10.0.0.5:0", "10.0.0.5:70000", "[fd12::1", "10.0.0.5:http",
		"fe80::1%eth0",
	} {
		_, err := egress.ParseAllow([]string{"10.0.0.5", entry})
		assert.ErrorContains(t, err, entry)
	}
}
