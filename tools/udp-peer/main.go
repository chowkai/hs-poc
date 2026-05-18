// udp-peer: Simple UDP tunnel peer for testing hs-poc data plane.
// Creates a TUN interface, reads IP packets from TUN, sends via UDP to peer,
// receives UDP packets from peer, writes to TUN.
//
// Usage:
//
//	udp-peer --tun tun0 --ip 100.64.0.1/24 --port 51820 --peer 100.64.0.2:51820
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.zx2c4.com/wireguard/tun"
)

func main() {
	tunName := flag.String("tun", "hs-tun", "TUN interface name")
	localIP := flag.String("ip", "100.64.0.1/24", "Local IP with CIDR (e.g. 100.64.0.1/24)")
	port := flag.Int("port", 51820, "UDP listen port")
	peerAddr := flag.String("peer", "", "Peer address (host:port)")
	flag.Parse()

	if *peerAddr == "" {
		log.Fatal("--peer is required (e.g. 100.64.0.2:51820)")
	}

	// Resolve peer
	peerUDP, err := net.ResolveUDPAddr("udp", *peerAddr)
	if err != nil {
		log.Fatalf("resolve peer: %v", err)
	}

	// Create TUN
	tunDev, err := tun.CreateTUN(*tunName, 1420)
	if err != nil {
		log.Fatalf("create TUN: %v", err)
	}
	defer tunDev.Close()

	// Assign IP and bring up
	exec.Command("ip", "addr", "add", *localIP, "dev", *tunName).Run()
	exec.Command("ip", "link", "set", "dev", *tunName, "up").Run()

	log.Printf("TUN %s up with IP %s", *tunName, *localIP)

	// Create UDP socket
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: *port})
	if err != nil {
		log.Fatalf("UDP listen: %v", err)
	}
	defer udpConn.Close()

	log.Printf("UDP listening on :%d, peer %s", *port, peerUDP)

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan struct{})

	// TUN → UDP
	go func() {
		buf := make([]byte, 65535)
		sizes := make([]int, 1)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := tunDev.Read([][]byte{buf}, sizes, 0)
			if err != nil {
				log.Printf("TUN read error: %v", err)
				return
			}
			if n > 0 {
				if _, err := udpConn.WriteToUDP(buf[:n], peerUDP); err != nil {
					log.Printf("UDP write error: %v", err)
				}
			}
		}
	}()

	// UDP → TUN
	go func() {
		buf := make([]byte, 65535)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, _, err := udpConn.ReadFromUDP(buf)
			if err != nil {
				log.Printf("UDP read error: %v", err)
				return
			}
			if n > 0 {
				if _, err := tunDev.Write([][]byte{buf[:n]}, 0); err != nil {
					log.Printf("TUN write error: %v", err)
				}
			}
		}
	}()

	log.Println("UDP tunnel running. Ctrl+C to stop.")
	<-sigCh
	close(done)
	log.Println("Shutting down...")
	exec.Command("ip", "link", "del", *tunName).Run()
}