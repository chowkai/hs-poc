package noise

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

const CurrentCapabilityVersion = 138
const UpgradeHeaderValue = "tailscale-control-protocol"
const HandshakeHeaderName = "X-Tailscale-Handshake"

type Dialer struct {
	Hostname   string
	MachineKey Key
	ControlKey Key
	Port       string
}

func (d *Dialer) Dial(ctx context.Context) (net.Conn, error) {
	if d.Port == "" {
		d.Port = "80"
	}

	protocolVersion := uint16(CurrentCapabilityVersion)
	init, cont, err := ClientDeferred(d.MachineKey, d.ControlKey, protocolVersion)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(d.Hostname, d.Port)

	var lastConn atomic.Value
	trace := httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			lastConn.Store(info.Conn)
		},
	}
	ctx = httptrace.WithClientTrace(ctx, &trace)

	tr := &http.Transport{
		DialContext:           (&net.Dialer{}).DialContext,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		MaxConnsPerHost:       1,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	defer tr.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addr+"/ts2021", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Upgrade", UpgradeHeaderValue)
	req.Header.Set("Connection", "upgrade")
	req.Header.Set(HandshakeHeaderName, base64.StdEncoding.EncodeToString(init))

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("noise dial failed: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected HTTP response: %s (body: %s)", resp.Status, string(body))
	}

	switchedConn := lastConn.Load()
	if switchedConn == nil {
		resp.Body.Close()
		return nil, fmt.Errorf("httptrace didn't provide a connection")
	}

	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, fmt.Errorf("http Transport did not provide a writable body")
	}

	netConn := switchedConn.(net.Conn)

	wrappedConn := &httpUpgradedConn{
		ReadWriteCloser: rwc,
		switchedConn:    netConn,
	}

	hs, err := cont(ctx, wrappedConn)
	if err != nil {
		rwc.Close()
		return nil, fmt.Errorf("noise handshake continuation failed: %w", err)
	}

	return NewConn(wrappedConn, hs.TX, hs.RX), nil
}

type httpUpgradedConn struct {
	io.ReadWriteCloser
	switchedConn net.Conn
}

func (c *httpUpgradedConn) LocalAddr() net.Addr                { return c.switchedConn.LocalAddr() }
func (c *httpUpgradedConn) RemoteAddr() net.Addr               { return c.switchedConn.RemoteAddr() }
func (c *httpUpgradedConn) SetDeadline(t time.Time) error      { return c.switchedConn.SetDeadline(t) }
func (c *httpUpgradedConn) SetReadDeadline(t time.Time) error  { return c.switchedConn.SetReadDeadline(t) }
func (c *httpUpgradedConn) SetWriteDeadline(t time.Time) error { return c.switchedConn.SetWriteDeadline(t) }