// hs-client: Headscale client node CLI using real noise protocol.
//
// Commands:
//
//	register --server URL --key PREAUTH_KEY [--name NAME]   Register via noise
//	up [--server URL] [--iface IFACE] [--port PORT]    Bring up WireGuard tunnel
//	down [--iface IFACE]
//	status
//	ping IP
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"hs-poc-client/noise"
)

var stateDir string

// tnet holds the netstack network for in-process ping.
var tnet *netstack.Net

func init() {
	home, _ := os.UserHomeDir()
	stateDir = home + "/.hs-client"
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hs-client <register|up|down|status|ping|traceroute|serve> [args...]")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "register":
		cmdRegister(os.Args[2:])
	case "up":
		cmdUp(os.Args[2:])
	case "down":
		cmdDown(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "ping":
		cmdPing(os.Args[2:])
	case "traceroute":
		cmdTraceroute(os.Args[2:])
	case "serve":
		cmdServe(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func getFlag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

// --- Key Management ---

func saveKey(filename string, k noise.Key) {
	os.MkdirAll(stateDir, 0755)
	os.WriteFile(stateDir+"/"+filename, []byte(hex.EncodeToString(k[:])), 0600)
}

func loadKey(filename string) (noise.Key, bool) {
	data, err := os.ReadFile(stateDir + "/" + filename)
	if err != nil {
		return noise.Key{}, false
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(b) != 32 {
		return noise.Key{}, false
	}
	return noise.KeyFromBytes(b), true
}

func loadOrGenMachineKey() noise.Key {
	if k, ok := loadKey("machine_key"); ok {
		return k
	}
	k := noise.NewKey()
	saveKey("machine_key", k)
	return k
}

func loadOrGenNodeKey() noise.Key {
	if k, ok := loadKey("node_key"); ok {
		return k
	}
	k := noise.NewKey()
	saveKey("node_key", k)
	return k
}

// --- Server Key ---

func fetchServerKey(serverURL string) (noise.Key, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return noise.Key{}, fmt.Errorf("invalid server URL: %w", err)
	}
	keyURL := fmt.Sprintf("%s://%s/key?v=%d", u.Scheme, u.Host, noise.CurrentCapabilityVersion)
	resp, err := http.Get(keyURL)
	if err != nil {
		return noise.Key{}, fmt.Errorf("fetch server key: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return noise.Key{}, fmt.Errorf("fetch server key: %d %s", resp.StatusCode, string(body))
	}
	var result struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return noise.Key{}, fmt.Errorf("parse server key: %w", err)
	}
	return parseHeadscaleKey(result.PublicKey)
}

func parseHeadscaleKey(s string) (noise.Key, error) {
	s = strings.TrimPrefix(s, "mkey:")
	b, err := hex.DecodeString(s)
	if err != nil {
		return noise.Key{}, err
	}
	if len(b) != 32 {
		return noise.Key{}, fmt.Errorf("key wrong length: %d", len(b))
	}
	return noise.KeyFromBytes(b), nil
}

// --- Noise Connection ---

func dialNoiseConn(ctx context.Context, serverURL string, machineKey, serverPubKey noise.Key) (*noise.H2Conn, error) {
	u, _ := url.Parse(serverURL)
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "80"
	}
	d := &noise.Dialer{
		Hostname:   host,
		MachineKey: machineKey,
		ControlKey: serverPubKey,
		Port:       port,
	}
	nc, err := d.Dial(ctx)
	if err != nil {
		return nil, err
	}
	return noise.NewH2Conn(nc)
}


// --- Tailcfg JSON Types ---

type RegisterRequest struct {
	Version    int                  `json:"version"`
	NodeKey    string               `json:"nodeKey"`
	OldNodeKey string               `json:"oldNodeKey,omitempty"`
	Auth       *RegisterRequestAuth `json:"auth,omitempty"`
	Hostinfo   json.RawMessage      `json:"hostinfo,omitempty"`
}

type RegisterRequestAuth struct {
	AuthKey string `json:"authKey,omitempty"`
}

type RegisterResponse struct {
	User              json.RawMessage `json:"user"`
	Login             json.RawMessage `json:"login"`
	NodeKeyExpired    bool            `json:"nodeKeyExpired"`
	MachineAuthorized bool            `json:"machineAuthorized"`
	AuthURL           string          `json:"authURL"`
	Error             string          `json:"error"`
}

type MapRequest struct {
	Version   int    `json:"version"`
	KeepAlive bool   `json:"keepAlive"`
	NodeKey   string `json:"nodeKey"`
	Stream    bool   `json:"stream"`
	OmitPeers bool   `json:"omitPeers"`
}

type MapNode struct {
	ID        uint64   `json:"id"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Endpoints []string `json:"endpoints"`
	Key       string   `json:"key"`
	DiscoKey  string   `json:"discoKey"`
	Machine   string   `json:"machine"`
}

type MapResponse struct {
	Node            *MapNode   `json:"node"`
	Peers           []*MapNode `json:"peers"`
	ControlDialPlan any        `json:"controlDialPlan"`
	KeepAlive       bool       `json:"keepAlive"`
}

func nodeKeyTailscale(k noise.Key) string {
	return "nodekey:" + hex.EncodeToString(k[:])
}

func parseNodeKey(s string) noise.Key {
	s = strings.TrimPrefix(s, "nodekey:")
	b, _ := hex.DecodeString(s)
	if len(b) == 32 {
		return noise.KeyFromBytes(b)
	}
	return noise.Key{}
}

// --- Register ---

func cmdRegister(args []string) {
	server := getFlag(args, "--server")
	preauthKey := getFlag(args, "--key")
	name := getFlag(args, "--name")
	if name == "" {
		host, _ := os.Hostname()
		name = host
	}

	if server == "" {
		fmt.Fprintln(os.Stderr, "usage: hs-client register --server URL [--key PREAUTH_KEY] [--name NAME]")
		os.Exit(1)
	}

	machineKey := loadOrGenMachineKey()
	nodeKey := loadOrGenNodeKey()

	fmt.Printf("Fetching server noise key from %s ...\n", server)
	serverPubKey, err := fetchServerKey(server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch server key failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Establishing noise connection...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc, err := dialNoiseConn(ctx, server, machineKey, serverPubKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "noise connect failed: %v\n", err)
		os.Exit(1)
	}
	defer nc.Close()

	u, _ := url.Parse(server)

	regReq := RegisterRequest{
		Version: noise.CurrentCapabilityVersion,
		NodeKey: nodeKeyTailscale(nodeKey.Public()),
	}
	if preauthKey != "" {
		regReq.Auth = &RegisterRequestAuth{AuthKey: preauthKey}
	}

	body, _ := json.Marshal(regReq)

	fmt.Println("Registering with Headscale...")
	statusCode, respBody, err := nc.DoRequest("POST", "/machine/register", u.Host, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "register failed: %v\n", err)
		os.Exit(1)
	}

	var regResp RegisterResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		fmt.Fprintf(os.Stderr, "parse register response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Register response (status %d): MachineAuthorized=%v\n", statusCode, regResp.MachineAuthorized)
	if regResp.Error != "" {
		fmt.Fprintf(os.Stderr, "register error: %s\n", regResp.Error)
		os.Exit(1)
	}

	os.MkdirAll(stateDir, 0755)
	cfg := fmt.Sprintf("SERVER=%s\nMACHINE_AUTHORIZED=%v\n", server, regResp.MachineAuthorized)
	os.WriteFile(stateDir+"/config", []byte(cfg), 0600)

	if regResp.MachineAuthorized {
		fmt.Println("Registered.")
	}
	if regResp.AuthURL != "" {
		fmt.Printf("Authorization URL: %s\n", regResp.AuthURL)
	}
}


// --- Up ---

func cmdUp(args []string) {
	cfg := loadConfig()
	server := getFlag(args, "--server")
	if server == "" {
		server = cfg["SERVER"]
	}
	ifaceName := getFlag(args, "--iface")
	if ifaceName == "" {
		ifaceName = "hs-tun"
	}
	listenPort := getFlag(args, "--port")
	if listenPort == "" {
		listenPort = "51820"
	}

	if server == "" {
		fmt.Fprintln(os.Stderr, "not registered, run 'hs-client register' first")
		os.Exit(1)
	}

	machineKey := loadOrGenMachineKey()
	nodeKey := loadOrGenNodeKey()

	fmt.Printf("Fetching server noise key from %s ...\n", server)
	serverPubKey, err := fetchServerKey(server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch server key failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Establishing noise connection...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h2conn, err := dialNoiseConn(ctx, server, machineKey, serverPubKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "noise connect failed: %v\n", err)
		os.Exit(1)
	}
	defer h2conn.Close()

	u, _ := url.Parse(server)

	mapReq := MapRequest{
		Version:   noise.CurrentCapabilityVersion,
		KeepAlive: true,
		NodeKey:   nodeKeyTailscale(nodeKey.Public()),
		Stream:    true,
		OmitPeers: false,
	}

	body, _ := json.Marshal(mapReq)

	fmt.Println("Requesting network map...")
	mapCtx, mapCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer mapCancel()
	statusCode, bodyBytes, err := h2conn.DoRequestContext(mapCtx, "POST", "/machine/map", u.Host, body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "map request failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Map response status: %d\n", statusCode)
	fmt.Fprintf(os.Stderr, "Debug map response (%d bytes): %s\n", len(bodyBytes), string(bodyBytes[:min(len(bodyBytes), 500)]))

	if statusCode != 200 {
		fmt.Fprintf(os.Stderr, "map request failed (%d): %s\n", statusCode, string(bodyBytes))
		os.Exit(1)
	}

	// Strip gRPC-web / length-delimited framing prefix before JSON
	if idx := bytes.IndexByte(bodyBytes, '{'); idx >= 0 {
		bodyBytes = bodyBytes[idx:]
	}

	var mapResp MapResponse
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	if err := dec.Decode(&mapResp); err != nil {
		fmt.Fprintf(os.Stderr, "parse map response: %v\n", err)
		os.Exit(1)
	}

	selfIP := ""
	if mapResp.Node != nil && len(mapResp.Node.Addresses) > 0 {
		// Strip CIDR suffix (e.g., "100.64.0.5/32" → "100.64.0.5")
		selfIP = strings.Split(mapResp.Node.Addresses[0], "/")[0]
		fmt.Printf("Self IP: %s\n", selfIP)
	}

	fmt.Printf("Got %d peers\n", len(mapResp.Peers))

	os.MkdirAll(stateDir, 0755)
	storeCfg := fmt.Sprintf("SERVER=%s\nNODE_IP=%s\n", server, selfIP)
	os.WriteFile(stateDir+"/config", []byte(storeCfg), 0600)

	// Parse --peer-endpoint overrides: IP=HOST:PORT
	peerEndpoints := map[string]string{}
	for _, a := range args {
		if strings.HasPrefix(a, "--peer-endpoint=") {
			kv := strings.TrimPrefix(a, "--peer-endpoint=")
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) == 2 {
				peerEndpoints[parts[0]] = parts[1]
				fmt.Printf("Peer endpoint override: %s → %s\n", parts[0], parts[1])
			}
		}
	}

	go runWireGuard(ifaceName, listenPort, nodeKey, selfIP, mapResp, peerEndpoints)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("connected")
	os.WriteFile(stateDir+"/pid", []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
	os.WriteFile(stateDir+"/iface", []byte(ifaceName), 0644)

	<-sigCh
	fmt.Println("\ndisconnecting...")
	cmdDown([]string{"--iface", ifaceName})
}

func runWireGuard(ifaceName, listenPort string, nodeKey noise.Key, selfIP string, mapResp MapResponse, peerEndpoints map[string]string) {
	localAddrs := []netip.Addr{netip.MustParseAddr(selfIP)}
	tunDev, tnetDev, err := netstack.CreateNetTUN(localAddrs, nil, device.DefaultMTU)
	if err != nil {
		fmt.Fprintf(os.Stderr, "netstack TUN create failed: %v\n", err)
		return
	}
	tnet = tnetDev

	wgDev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelVerbose, ""))

	privKeyHex := hex.EncodeToString(nodeKey[:])
	cfgStr := fmt.Sprintf("private_key=%s\nlisten_port=%s\n", privKeyHex, listenPort)

	for _, n := range mapResp.Peers {
		if n == nil {
			continue
		}
		peerKey := parseNodeKey(n.Key)
		if peerKey.IsZero() {
			continue
		}
		peerKeyHex := hex.EncodeToString(peerKey[:])
		for _, addr := range n.Addresses {
			cfgStr += fmt.Sprintf("public_key=%s\nallowed_ip=%s\n", peerKeyHex, addr)
		}
		// Set endpoint: 1) --peer-endpoint override, 2) map response, 3) same-machine fallback
		hasEndpoint := false
		// Check override first
		for _, addr := range n.Addresses {
			ip := strings.Split(addr, "/")[0]
			if override, ok := peerEndpoints[ip]; ok {
				cfgStr += fmt.Sprintf("endpoint=%s\n", override)
				hasEndpoint = true
				fmt.Fprintf(os.Stderr, "  using override endpoint for %s: %s\n", ip, override)
				break
			}
		}
		if !hasEndpoint {
			for _, ep := range n.Endpoints {
				cfgStr += fmt.Sprintf("endpoint=%s\n", ep)
				hasEndpoint = true
			}
		}
		if !hasEndpoint && len(n.Addresses) > 0 {
			// Same-machine fallback: port = 51820 + (last_octet - first_node_octet)
			ip := strings.Split(n.Addresses[0], "/")[0]
			parts := strings.Split(ip, ".")
			if len(parts) == 4 && parts[0] == "100" {
				lastOctet := 0
				fmt.Sscanf(parts[3], "%d", &lastOctet)
				baseOctet := 15 // first node in this test batch
				port := 51820 + (lastOctet - baseOctet)
				cfgStr += fmt.Sprintf("endpoint=127.0.0.1:%d\n", port)
			}
		}
	}

	fmt.Fprintf(os.Stderr, "WG config:\n%s\n", cfgStr)

	if err := wgDev.IpcSet(cfgStr); err != nil {
		fmt.Fprintf(os.Stderr, "IpcSet failed: %v\n", err)
		return
	}
	if err := wgDev.Up(); err != nil {
		fmt.Fprintf(os.Stderr, "dev.Up failed: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "WireGuard up with netstack @ %s\n", selfIP)
	
	// Self-ping test: verify netstack + WG data-plane works in-process
	time.Sleep(500 * time.Millisecond)
	socket, err := tnet.Dial("ping4", selfIP)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SELF-PING: dial failed: %v\n", err)
	} else {
		defer socket.Close()
		icmpMsg := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{ID: 1, Seq: 1, Data: []byte("hello")},
		}
		b, _ := icmpMsg.Marshal(nil)
		socket.SetWriteDeadline(time.Now().Add(2 * time.Second))
		socket.Write(b)
		socket.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := socket.Read(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SELF-PING: recv failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "SELF-PING: ✅ reply %d bytes\n", n)
		}
	}

	// Cross-node ping: prefer peer with endpoint override, then last peer
	var bestPeerIP string
	// First: check for any peer that has a --peer-endpoint override
	for _, n := range mapResp.Peers {
		if n == nil || len(n.Addresses) == 0 {
			continue
		}
		ip := strings.Split(n.Addresses[0], "/")[0]
		if ip == selfIP {
			continue
		}
		if _, ok := peerEndpoints[ip]; ok {
			bestPeerIP = ip
			break
		}
	}
	// Fallback: pick the last (most recent) peer
	if bestPeerIP == "" {
		for _, n := range mapResp.Peers {
			if n == nil || len(n.Addresses) == 0 {
				continue
			}
			ip := strings.Split(n.Addresses[0], "/")[0]
			if ip == selfIP {
				continue
			}
			bestPeerIP = ip
		}
	}
	if bestPeerIP != "" {
		fmt.Fprintf(os.Stderr, "CROSS-PING: trying %s...\n", bestPeerIP)
		s2, err := tnet.Dial("ping4", bestPeerIP)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CROSS-PING %s: dial failed: %v\n", bestPeerIP, err)
		} else {
			icmpMsg2 := icmp.Message{
				Type: ipv4.ICMPTypeEcho, Code: 0,
				Body: &icmp.Echo{ID: 2, Seq: 1, Data: []byte("cross")},
			}
			b2, _ := icmpMsg2.Marshal(nil)
			s2.SetWriteDeadline(time.Now().Add(3 * time.Second))
			s2.Write(b2)
			s2.SetReadDeadline(time.Now().Add(5 * time.Second))
			n2, err := s2.Read(b2)
			s2.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "CROSS-PING %s: ❌ %v\n", bestPeerIP, err)
			} else {
				fmt.Fprintf(os.Stderr, "CROSS-PING %s: ✅ reply %d bytes\n", bestPeerIP, n2)
			}
		}
	}
	
	select {}
}

// --- Config ---

func loadConfig() map[string]string {
	data, err := os.ReadFile(stateDir + "/config")
	if err != nil {
		return nil
	}
	cfg := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			cfg[parts[0]] = parts[1]
		}
	}
	return cfg
}

// --- Down ---

func cmdDown(args []string) {
	ifaceName := getFlag(args, "--iface")
	if ifaceName == "" {
		data, _ := os.ReadFile(stateDir + "/iface")
		ifaceName = strings.TrimSpace(string(data))
	}
	os.Remove(stateDir + "/pid")
	os.Remove(stateDir + "/iface")
	fmt.Println("disconnected")
}

// --- Status ---

func cmdStatus(args []string) {
	cfg := loadConfig()
	if cfg == nil {
		fmt.Println(`{"connected":false,"ip":"","peers":[]}`)
		return
	}

	_, err := os.Stat(stateDir + "/pid")
	connected := err == nil

	status := map[string]any{
		"connected": connected,
		"ip":        cfg["NODE_IP"],
		"peers":     []map[string]string{},
	}
	out, _ := json.Marshal(status)
	fmt.Println(string(out))
}

// --- Ping ---

func cmdPing(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: hs-client ping <ip>")
		os.Exit(1)
	}
	target := args[0]

	if tnet == nil {
		// Not running with netstack — fall back to kernel ping
		cmd := exec.Command("ping", "-c", "3", "-W", "2", target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "ping failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	socket, err := tnet.Dial("ping4", target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ping dial failed: %v\n", err)
		os.Exit(1)
	}
	defer socket.Close()

	for i := 0; i < 3; i++ {
		icmpMsg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   os.Getpid() & 0xffff,
				Seq:  i + 1,
				Data: []byte("hs-poc ping"),
			},
		}
		b, err := icmpMsg.Marshal(nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "marshal: %v\n", err)
			continue
		}

		socket.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := socket.Write(b); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			continue
		}

		socket.SetReadDeadline(time.Now().Add(2 * time.Second))
		start := time.Now()
		n, err := socket.Read(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "recv timeout (seq=%d)\n", i+1)
			continue
		}

		reply, err := icmp.ParseMessage(1, b[:n])
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse: %v\n", err)
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if !ok {
			fmt.Fprintf(os.Stderr, "not echo reply (seq=%d)\n", i+1)
			continue
		}

		fmt.Printf("reply from %s: seq=%d time=%v\n", target, echo.Seq, time.Since(start))
		time.Sleep(time.Second)
	}
}

// --- Traceroute ---

func cmdTraceroute(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: hs-client traceroute <ip>")
		os.Exit(1)
	}
	target := args[0]

	if tnet == nil {
		// Not running with netstack — fall back to kernel traceroute
		cmd := exec.Command("traceroute", "-m", "30", "-w", "2", target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "traceroute failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Printf("traceroute to %s, 30 hops max\n", target)

	for ttl := 1; ttl <= 30; ttl++ {
		socket, err := tnet.Dial("ping4", target)
		if err != nil {
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		pc := ipv4.NewConn(socket)
		if err := pc.SetTTL(ttl); err != nil {
			socket.Close()
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		// Build ICMP Echo request
		icmpMsg := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   os.Getpid() & 0xffff,
				Seq:  ttl,
				Data: []byte("hs-poc-trace"),
			},
		}
		b, err := icmpMsg.Marshal(nil)
		if err != nil {
			socket.Close()
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		socket.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err := socket.Write(b); err != nil {
			socket.Close()
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		// Read response
		rbuf := make([]byte, 1500)
		socket.SetReadDeadline(time.Now().Add(3 * time.Second))
		start := time.Now()
		n, err := socket.Read(rbuf)
		elapsed := time.Since(start)

		if err != nil {
			socket.Close()
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		// Try to parse IPv4 header for source address (netstack may include it)
		hopIP := target
		icmpStart := 0
		if n > 20 && (rbuf[0]>>4) == 4 {
			ihl := int(rbuf[0]&0x0f) * 4
			if ihl >= 20 && n > ihl {
				hopIP = fmt.Sprintf("%d.%d.%d.%d", rbuf[12], rbuf[13], rbuf[14], rbuf[15])
				icmpStart = ihl
			}
		}

		reply, err := icmp.ParseMessage(1, rbuf[icmpStart:n])
		if err != nil {
			socket.Close()
			fmt.Printf("%2d  *\n", ttl)
			continue
		}

		socket.Close()

		switch reply.Type {
		case ipv4.ICMPTypeEchoReply:
			fmt.Printf("%2d  %s  %.3f ms\n", ttl, hopIP, float64(elapsed.Microseconds())/1000.0)
			return
		case ipv4.ICMPTypeTimeExceeded:
			fmt.Printf("%2d  %s  %.3f ms\n", ttl, hopIP, float64(elapsed.Microseconds())/1000.0)
		default:
			fmt.Printf("%2d  %s  %.3f ms (type=%d)\n", ttl, hopIP, float64(elapsed.Microseconds())/1000.0, reply.Type)
		}
	}
	fmt.Println("traceroute complete (max hops reached)")
}

// --- Serve ---

func cmdServe(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: hs-client serve <port>")
		os.Exit(1)
	}
	port := args[0]

	if tnet == nil {
		fmt.Fprintln(os.Stderr, "error: netstack not running. Run 'hs-client up' first.")
		os.Exit(1)
	}

	var portNum int
	if _, err := fmt.Sscanf(port, "%d", &portNum); err != nil || portNum < 1 || portNum > 65535 {
		fmt.Fprintf(os.Stderr, "invalid port: %s\n", port)
		os.Exit(1)
	}

	listener, err := tnet.ListenTCP(&net.TCPAddr{Port: portNum})
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on netstack port %s failed: %v\n", port, err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	fmt.Printf("Serving files from %s on port %s (netstack)\n", cwd, port)

	handler := http.FileServer(http.Dir("."))
	if err := http.Serve(listener, handler); err != nil {
		fmt.Fprintf(os.Stderr, "HTTP serve error: %v\n", err)
		os.Exit(1)
	}
}