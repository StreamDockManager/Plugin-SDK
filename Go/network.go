package sdk

import (
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/payload"
	"net"
	"sync"
)

// NetworkPayload excludes SDK loopback, TLS/TCP overhead, DNS, subprocesses,
// and library-owned transports (including speedtest-go)
func NetworkPayload() (sent, received uint64) { return payload.Snapshot() }

type countingConn struct {
	net.Conn
	closed sync.Once
}

// Read socket bytes and account for received payload
func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	payload.Received.Add(uint64(n))
	if n > 0 {
		netdebug.IO(c.RemoteAddr(), "received", n)
	}
	return n, err
}

// Write socket bytes and account for sent payload
func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	payload.Sent.Add(uint64(n))
	if n > 0 {
		netdebug.IO(c.RemoteAddr(), "sent", n)
	}
	return n, err
}

// Close the tracked connection
func (c *countingConn) Close() error {
	err := c.Conn.Close()
	c.closed.Do(func() { netdebug.IO(c.RemoteAddr(), "closed", 0) })
	return err
}

// Wrap a connection with payload accounting
func TrackConn(connection net.Conn) net.Conn {
	if connection == nil {
		return nil
	}
	return &countingConn{Conn: connection}
}
