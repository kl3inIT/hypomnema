// Package egress limits what the shared browser may connect to (issue #202):
// every address a destination resolves to gets a class, and only the public
// internet and what the owner listed pass.
//
// Adapted from OpenMausBot (electron/egress-addresses.mjs and
// server/browser/egress/policy.ts and gate.ts, Apache License 2.0, Copyright
// 2026 Milind Soni and OpenMausBot contributors; its notice is in
// third_party/openmausbot/NOTICE), which adapted the address ranges and the
// SOCKS5 server from T3 Code (apps/server/src/htmlRender/publicProxy.ts, MIT
// License, Copyright (c) 2026 T3 Tools Inc.; third_party/t3code/LICENSE).
// Changed: Go in place of Node, and only the rules of a self-hosted server
// are kept.
package egress

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Refusal is why a connection is refused; empty when it may go ahead.
type Refusal string

// The reasons a connection is refused.
const (
	Allowed Refusal = ""
	// OwnService is a port the control plane or the browser itself listens on.
	OwnService Refusal = "the control plane's own service"
	// ThisComputer is another service on the machine the browser runs on.
	ThisComputer Refusal = "a service on the browser's own machine"
	// LocalNetwork is an address on a private network.
	LocalNetwork Refusal = "the local network"
	// LinkLocal is a link-local address.
	LinkLocal Refusal = "a link-local address"
	// Metadata is a cloud provider's metadata service.
	Metadata Refusal = "the cloud metadata service"
	// Reserved is an address that is not one host on the internet.
	Reserved Refusal = "a reserved address"
)

type class int

const (
	public class = iota
	loopback
	thisMachine
	private
	linkLocal
	metadata
	reserved
)

func prefixes(texts ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(texts))
	for i, text := range texts {
		out[i] = netip.MustParsePrefix(text)
	}

	return out
}

var (
	loopbackRanges = prefixes("127.0.0.0/8", "::1/128")
	// ::/96 (IPv4-compatible, which also holds the unspecified address), local-use NAT64 and Teredo carry an
	// IPv4 address a translator may route to without checking it.
	reservedRanges = prefixes("0.0.0.0/8", "198.18.0.0/15", "224.0.0.0/3",
		"::/96", "64:ff9b:1::/48", "2001::/32", "ff00::/8")
	linkLocalRanges = prefixes("169.254.0.0/16", "fe80::/10")
	privateRanges   = prefixes("10.0.0.0/8", "100.64.0.0/10", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
	// AWS (IPv4 and IPv6), Azure, GCP and Oracle answer on 169.254.169.254; Alibaba on 100.100.100.200.
	metadataRanges = prefixes("169.254.169.254/32", "fd00:ec2::254/128", "100.100.100.200/32")

	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func within(ranges []netip.Prefix, address netip.Addr) bool {
	for _, r := range ranges {
		if r.Contains(address) {
			return true
		}
	}

	return false
}

// embedded is the IPv4 address a NAT64 or 6to4 address stands for, which a
// translator will route to.
func embedded(address netip.Addr) (netip.Addr, bool) {
	raw := address.As16()

	switch {
	case nat64.Contains(address):
		return netip.AddrFrom4([4]byte(raw[12:16])), true
	case sixToFour.Contains(address):
		return netip.AddrFrom4([4]byte(raw[2:6])), true
	}

	return netip.Addr{}, false
}

// classOf is the kind of place an address is. own holds the addresses of this
// machine's interfaces.
func classOf(address netip.Addr, own map[netip.Addr]bool) class {
	if !address.IsValid() {
		return reserved
	}

	// An IPv4-mapped IPv6 address is its IPv4 address.
	address = address.WithZone("").Unmap()

	if address.Is6() {
		if inner, ok := embedded(address); ok {
			return classOf(inner, own)
		}
	}

	switch {
	case within(metadataRanges, address):
		return metadata
	case within(loopbackRanges, address):
		return loopback
	case within(reservedRanges, address):
		return reserved
	case within(linkLocalRanges, address):
		return linkLocal
	case own[address]:
		return thisMachine
	case within(privateRanges, address):
		return private
	}

	return public
}

// OwnAddresses are the addresses this machine's interfaces hold now.
func OwnAddresses() map[netip.Addr]bool {
	own := map[netip.Addr]bool{}

	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return own
	}

	for _, a := range addresses {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil {
			own[prefix.Addr().WithZone("").Unmap()] = true
		}
	}

	return own
}

// entry is one line of the allow list: a network, and a port or 0 for any.
type entry struct {
	networks []netip.Prefix
	port     uint16
}

// Allow is the list of private destinations the owner lets the browser reach.
type Allow struct {
	entries []entry
}

var (
	bracketed = regexp.MustCompile(`^\[([^\]]+)\](?:/(\d{1,3}))?(?::(\d{1,5}))?$`)
	bareV6    = regexp.MustCompile(`^([^/]+)(?:/(\d{1,3}))?()$`)
	plain     = regexp.MustCompile(`^([^/:]+)(?:/(\d{1,3}))?(?::(\d{1,5}))?$`)
)

// ParseAllow reads the allow list: entries such as `10.0.0.5`,
// `192.168.1.0/24`, `10.0.0.5:8080`, `fd12::/48` and `[fd12::1]:443`.
func ParseAllow(entries []string) (Allow, error) {
	var allow Allow

	for _, text := range entries {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}

		e, err := parseEntry(text)
		if err != nil {
			return Allow{}, fmt.Errorf("%q: %w", text, err)
		}

		allow.entries = append(allow.entries, e)
	}

	return allow, nil
}

func parseEntry(text string) (entry, error) {
	const shape = "must be an IP address or a CIDR range, with an optional :port"

	match := bracketed.FindStringSubmatch(text)
	if match == nil {
		// A bare IPv6 address has colons of its own, so only its bracketed form takes a port.
		if strings.Count(text, ":") > 1 {
			match = bareV6.FindStringSubmatch(text)
		} else {
			match = plain.FindStringSubmatch(text)
		}
	}

	if match == nil {
		return entry{}, fmt.Errorf("%s", shape)
	}

	address, err := netip.ParseAddr(match[1])
	if err != nil || address.Zone() != "" {
		return entry{}, fmt.Errorf("%s", shape)
	}

	address = address.Unmap()
	bits := address.BitLen()

	if match[2] != "" {
		if bits, err = strconv.Atoi(match[2]); err != nil || bits > address.BitLen() {
			return entry{}, fmt.Errorf("the prefix is longer than the address")
		}
	}

	e := entry{networks: []netip.Prefix{netip.PrefixFrom(address, bits).Masked()}}

	if match[3] != "" {
		port, err := strconv.ParseUint(match[3], 10, 16)
		if err != nil || port == 0 {
			return entry{}, fmt.Errorf("the port must be between 1 and 65535")
		}

		e.port = uint16(port)

		// `localhost` is both 127.0.0.1 and ::1, so an entry for one loopback address and port covers the other.
		switch e.networks[0] {
		case netip.MustParsePrefix("127.0.0.1/32"):
			e.networks = append(e.networks, netip.MustParsePrefix("::1/128"))
		case netip.MustParsePrefix("::1/128"):
			e.networks = append(e.networks, netip.MustParsePrefix("127.0.0.1/32"))
		}
	}

	return e, nil
}

func (a Allow) has(address netip.Addr, port uint16) bool {
	address = address.WithZone("").Unmap()

	for _, e := range a.entries {
		if (e.port == 0 || e.port == port) && within(e.networks, address) {
			return true
		}
	}

	return false
}

// Rules decide what the browser may connect to.
type Rules struct {
	// Allow is the owner's list of private destinations.
	Allow Allow
	// OwnPort reports a port the control plane or the browser itself listens
	// on: on this machine it never opens, whatever Allow says.
	OwnPort func(port uint16) bool
}

// Refusal is why a connection to port at addresses, the addresses a
// destination resolved to, is refused. Every address must pass: the gate
// connects only to addresses checked here.
func (r Rules) Refusal(port uint16, addresses []netip.Addr, own map[netip.Addr]bool) Refusal {
	for _, address := range addresses {
		switch classOf(address, own) {
		case public:
		case metadata:
			return Metadata
		case linkLocal:
			return LinkLocal
		case reserved:
			return Reserved
		case private:
			if !r.Allow.has(address, port) {
				return LocalNetwork
			}
		case loopback, thisMachine:
			if r.OwnPort != nil && r.OwnPort(port) {
				return OwnService
			}

			if !r.Allow.has(address, port) {
				return ThisComputer
			}
		}
	}

	return Allowed
}

var localhostName = regexp.MustCompile(`(?i)^(?:[^.]+\.)*localhost\.?$`)

// namesThisMachine reports a name RFC 6761 and Chrome resolve to loopback
// themselves.
func namesThisMachine(host string) bool {
	return localhostName.MatchString(host)
}
