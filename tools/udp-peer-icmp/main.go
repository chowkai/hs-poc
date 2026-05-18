// udp-peer-icmp: UDP tunnel peer that responds to ICMP echo requests.
// Receives raw IP packets via UDP, swaps src/dst, flips ICMP type from request to reply.
package main

import (
	"encoding/binary"
	"log"
	"net"
)

func main() {
	port := 51821
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	log.Printf("ICMP peer listening on :%d", port)

	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read: %v", err)
			continue
		}
		if n < 28 {
			continue // too short for IP+ICMP
		}

		// Parse IP header
		ipHdr := buf[:n]
		if ipHdr[0]>>4 != 4 {
			continue // IPv4 only
		}
		ipLen := int(binary.BigEndian.Uint16(ipHdr[2:4]))
		if ipLen > n {
			ipLen = n
		}

		// Build reply: copy original, swap src/dst, fix ICMP
		reply := make([]byte, ipLen)
		copy(reply, ipHdr[:ipLen])

		// Swap IP src (bytes 12-15) and dst (bytes 16-19)
		copy(reply[12:16], ipHdr[16:20]) // src ← dst
		copy(reply[16:20], ipHdr[12:16]) // dst ← src

		// Zero IP checksum and recalculate
		reply[10] = 0
		reply[11] = 0
		cs := ipChecksum(reply[:20])
		reply[10] = byte(cs >> 8)
		reply[11] = byte(cs)

		// Fix ICMP: change type from 8 (echo request) to 0 (echo reply)
		icmpOff := int(ipHdr[0]&0x0f) * 4
		if reply[icmpOff] == 8 {
			reply[icmpOff] = 0 // echo request → echo reply

			// Zero ICMP checksum and recalculate
			reply[icmpOff+2] = 0
			reply[icmpOff+3] = 0
			icmpCS := ipChecksum(reply[icmpOff : icmpOff+ipLen-icmpOff])
			reply[icmpOff+2] = byte(icmpCS >> 8)
			reply[icmpOff+3] = byte(icmpCS)
		}

		conn.WriteToUDP(reply, addr)
		log.Printf("ICMP reply %d bytes to %s", ipLen, addr)
	}
}

func ipChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i < len(b)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 != 0 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return uint16(^sum)
}