// udp-echo: Simple UDP echo server for testing data plane.
// Receives any UDP packet and echoes it back to the sender.
package main

import (
	"log"
	"net"
)

func main() {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 51820})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	log.Println("UDP echo listening on :51820")

	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read error: %v", err)
			continue
		}
		log.Printf("echo %d bytes from %s", n, addr)
		conn.WriteToUDP(buf[:n], addr)
	}
}