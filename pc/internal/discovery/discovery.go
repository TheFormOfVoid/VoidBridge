// Package discovery lets phones find the PC on the local network.
//
// The PC broadcasts a small JSON beacon on UDP port 47830 every couple of
// seconds, and also answers "probe" datagrams sent to that port directly, so a
// phone that has just joined the network doesn't have to wait for the next
// beacon. Beacons carry a short fingerprint of the pairing key so a phone can
// ignore VoidBridge PCs it isn't paired with.
package discovery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"time"
)

const (
	Port     = 47830
	Interval = 2 * time.Second
)

// Beacon is the datagram the PC sends.
type Beacon struct {
	App         string `json:"app"` // always "voidbridge"
	ID          string `json:"id"`
	Name        string `json:"name"`
	Port        int    `json:"port"`
	Fingerprint string `json:"fp"`
}

// Probe is what a phone sends to ask PCs to announce themselves.
type Probe struct {
	App  string `json:"app"`
	Type string `json:"type"` // "probe"
}

// Fingerprint identifies a pairing key without revealing it.
func Fingerprint(pairingKey []byte) string {
	m := hmac.New(sha256.New, pairingKey)
	m.Write([]byte("voidbridge-beacon-v1"))
	return hex.EncodeToString(m.Sum(nil)[:8])
}

// Announce broadcasts b until stop is closed, and answers probes.
func Announce(b Beacon, stop <-chan struct{}, logf func(string, ...any)) {
	b.App = "voidbridge"
	payload, _ := json.Marshal(b)

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: Port})
	if err != nil {
		// Port busy: still broadcast from an ephemeral port, just without
		// answering probes.
		logf("discovery: can't listen on udp %d (%v); broadcasting only", Port, err)
		conn, err = net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			logf("discovery: disabled: %v", err)
			return
		}
	}
	go func() {
		<-stop
		conn.Close()
	}()

	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				select {
				case <-stop:
					return
				default:
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var p Probe
			if json.Unmarshal(buf[:n], &p) == nil && p.App == "voidbridge" && p.Type == "probe" {
				conn.WriteToUDP(payload, from)
			}
		}
	}()

	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		for _, dst := range broadcastAddrs() {
			conn.WriteToUDP(payload, &net.UDPAddr{IP: dst, Port: Port})
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// broadcastAddrs returns the limited broadcast address plus the directed
// broadcast address of every active IPv4 interface. Some routers drop one kind
// but not the other.
func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || len(ipn.Mask) != 4 {
				continue
			}
			bc := make(net.IP, 4)
			for i := range bc {
				bc[i] = ip4[i] | ^ipn.Mask[i]
			}
			out = append(out, bc)
		}
	}
	return out
}

// LocalIPs lists this machine's private IPv4 addresses, for showing to the
// user as a manual fallback.
func LocalIPs() []string {
	var out []string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip4 := ipn.IP.To4(); ip4 != nil && ip4.IsPrivate() {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}
