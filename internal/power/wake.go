// Package power implements Wake-on-LAN. A powered-off agent cannot receive
// HTTP commands; another node on its broadcast domain sends the magic packet.
package power

import (
	"fmt"
	"net"
)

func Packet(mac string) ([]byte, error) {
	addr, err := net.ParseMAC(mac)
	if err != nil || len(addr) != 6 {
		return nil, fmt.Errorf("a six-byte Ethernet MAC is required")
	}
	p := make([]byte, 102)
	for i := 0; i < 6; i++ {
		p[i] = 255
	}
	for i := 6; i < len(p); i += 6 {
		copy(p[i:], addr)
	}
	return p, nil
}
func Wake(mac string) error {
	p, err := Packet(mac)
	if err != nil {
		return err
	}
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: 9})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write(p)
	return err
}
