// hs-server: Adapter bridging wireguard-go hs-client to real Headscale.
//
// Manages WireGuard keypairs and node assignment independently.
// Syncs with Headscale via its gRPC-gateway REST API for user/API-key management.
//
// API:
//   POST /api/v1/node/register?name=X&endpoint=Y  generate WG keys, assign IP
//   GET  /api/v1/node                              list all nodes
//   GET  /api/v1/node/{id}                         get single node with private key
//   POST /api/v1/node/{id}/keepalive               update last_seen
//
// Admin:
//   POST /admin/init                               create headscale user + API key
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/curve25519"
)

var stateDir = "./hs-data"

func main() {
	if v := os.Getenv("DATA_DIR"); v != "" {
		stateDir = v
	}
	os.MkdirAll(stateDir, 0755)

	listen := ":8080"
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		listen = v
	}
	hsURL := "http://127.0.0.1:8081"
	if v := os.Getenv("HEADSCALE_URL"); v != "" {
		hsURL = v
	}
	adminKey := "poc-admin-key-change-me"
	if v := os.Getenv("ADMIN_KEY"); v != "" {
		adminKey = v
	}

	store := &nodeStore{}
	store.load(stateDir)

	a := &Adapter{
		hsURL:    hsURL,
		hsAPIKey: adminKey,
		store:    store,
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("/api/v1/node", a.authMiddleware(a.handleNodes))
	mux.HandleFunc("/api/v1/node/", a.authMiddleware(a.handleNodeByID))
	mux.HandleFunc("/api/v1/node/register", a.handleRegister)
	mux.HandleFunc("/admin/init", a.handleInit)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Adapter listening on %s (Headscale: %s)", listen, hsURL)
		if err := http.ListenAndServe(listen, mux); err != nil {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")
}

// --- Node ---

type Node struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IP         string `json:"ip"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	Endpoint   string `json:"endpoint"`
	LastSeen   string `json:"last_seen"`
}

// --- Node Store ---

type nodeStore struct {
	mu     sync.RWMutex
	nodes  []Node
	nextID int
}

func (s *nodeStore) load(dir string) {
	data, err := os.ReadFile(dir + "/nodes.json")
	if err != nil {
		s.nextID = 2
		return
	}
	json.Unmarshal(data, &s.nodes)
	if len(s.nodes) > 0 {
		s.nextID = len(s.nodes) + 2
	} else {
		s.nextID = 2
	}
}

func (s *nodeStore) save(dir string) {
	data, _ := json.Marshal(s.nodes)
	os.WriteFile(dir+"/nodes.json", data, 0644)
}

// --- Adapter ---

type Adapter struct {
	hsURL    string
	hsAPIKey string
	store    *nodeStore
}

func (a *Adapter) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			auth = r.URL.Query().Get("key")
		}
		auth = strings.TrimPrefix(auth, "Bearer ")
		if auth != a.hsAPIKey {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}
		next(w, r)
	}
}

// GET /api/v1/node — list all nodes (without private keys)
func (a *Adapter) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.store.mu.RLock()
	nodes := make([]Node, len(a.store.nodes))
	copy(nodes, a.store.nodes)
	a.store.mu.RUnlock()

	if nodes == nil {
		nodes = []Node{}
	}

	cleaned := make([]Node, len(nodes))
	for i, n := range nodes {
		cleaned[i] = n
		cleaned[i].PrivateKey = ""
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"nodes": cleaned})
}

// GET /api/v1/node/{id}  or  POST /api/v1/node/{id}/keepalive
func (a *Adapter) handleNodeByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing node ID", 400)
		return
	}
	nodeID := parts[0]

	switch {
	case len(parts) >= 2 && parts[1] == "keepalive" && r.Method == "POST":
		a.store.mu.Lock()
		for i := range a.store.nodes {
			if a.store.nodes[i].ID == nodeID {
				a.store.nodes[i].LastSeen = time.Now().UTC().Format(time.RFC3339)
				break
			}
		}
		a.store.save(stateDir)
		a.store.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	case r.Method == "GET":
		a.store.mu.RLock()
		var node *Node
		for i := range a.store.nodes {
			if a.store.nodes[i].ID == nodeID {
				n := a.store.nodes[i]
				node = &n
				break
			}
		}
		a.store.mu.RUnlock()

		if node == nil {
			http.Error(w, `{"error":"node not found"}`, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"node": node})

	default:
		http.Error(w, "method not allowed", 405)
	}
}

// POST /api/v1/node/register?name=X&endpoint=Y
func (a *Adapter) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}

	auth := r.Header.Get("Authorization")
	auth = strings.TrimPrefix(auth, "Bearer ")
	if auth != a.hsAPIKey {
		http.Error(w, `{"error":"unauthorized"}`, 401)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		name = generateNodeName()
	}
	endpoint := r.URL.Query().Get("endpoint")

	_, _, privKey, pubKey := generateKeyPair()

	a.store.mu.Lock()
	id := fmt.Sprintf("node-%d", a.store.nextID)
	ip := fmt.Sprintf("100.64.0.%d", a.store.nextID)
	a.store.nextID++

	node := Node{
		ID:         id,
		Name:       name,
		IP:         ip,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		Endpoint:   endpoint,
		LastSeen:   time.Now().UTC().Format(time.RFC3339),
	}
	a.store.nodes = append(a.store.nodes, node)
	a.store.save(stateDir)
	a.store.mu.Unlock()

	go a.syncToHeadscale(node)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"node": node})
}

func generateKeyPair() (priv []byte, pub []byte, privB64 string, pubB64 string) {
	priv = make([]byte, 32)
	rand.Read(priv)
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, _ = curve25519.X25519(priv, curve25519.Basepoint)
	privB64 = base64.StdEncoding.EncodeToString(priv)
	pubB64 = base64.StdEncoding.EncodeToString(pub)
	_ = priv // used below
	return
}

func (a *Adapter) syncToHeadscale(node Node) {
	body := map[string]any{"user": "default", "key": node.ID, "name": node.Name}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", a.hsURL+"/api/v1/node/register", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.hsAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("sync to headscale: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("sync to headscale %d: %s", resp.StatusCode, string(bodyBytes))
	} else {
		log.Printf("synced node %s to headscale", node.ID)
	}
}

func (a *Adapter) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	results := map[string]string{}

	// Create user
	userBody := map[string]string{"name": "default"}
	data, _ := json.Marshal(userBody)
	req, _ := http.NewRequest("POST", a.hsURL+"/api/v1/user", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if a.hsAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.hsAPIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		results["user"] = fmt.Sprintf("error: %v", err)
	} else {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		results["user"] = fmt.Sprintf("%d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Create API key
	apiBody := map[string]any{}
	data, _ = json.Marshal(apiBody)
	req2, _ := http.NewRequest("POST", a.hsURL+"/api/v1/apikey", bytes.NewReader(data))
	req2.Header.Set("Content-Type", "application/json")
	if a.hsAPIKey != "" {
		req2.Header.Set("Authorization", "Bearer "+a.hsAPIKey)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		results["apikey"] = fmt.Sprintf("error: %v", err)
	} else {
		body2, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		results["apikey"] = string(body2)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func generateNodeName() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("node-%x", b)
}