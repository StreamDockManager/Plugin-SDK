package sdk

// The host transport uses coder/websocket while retaining the local-only dial
// policy and exact JSON byte accounting of the Manager protocol
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const maxUpgradeBytes = 64 << 10

type wireConn struct {
	conn *websocket.Conn
	ctx  context.Context
}

// Dial only the validated loopback host WebSocket
func dialLoopback(parent context.Context, address string) (*wireConn, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "ws" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.User != nil || u.Fragment != "" {
		return nil, errors.New("host must be loopback ws")
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, errors.New("invalid loopback port")
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	transport := &http.Transport{
		// Never use environment proxies or redirects for authenticated host traffic
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxUpgradeBytes,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			addresses := []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}
			var err error
			if u.Hostname() == "localhost" {
				addresses, err = net.DefaultResolver.LookupIPAddr(ctx, "localhost")
				if err != nil {
					return nil, err
				}
			}
			for _, address := range addresses {
				if !address.IP.IsLoopback() {
					continue
				}
				var conn net.Conn
				conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(address.String(), port))
				if err == nil {
					return conn, nil
				}
			}
			return nil, fmt.Errorf("cannot connect to loopback host: %w", errors.Join(err, ctx.Err(), errors.New("no reachable loopback address")))
		},
	}
	defer transport.CloseIdleConnections()
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(MaxMessageBytes)
	return &wireConn{conn: conn, ctx: parent}, nil
}

// Close the underlying WebSocket immediately
func (c *wireConn) Close() error { return c.conn.CloseNow() }

// Set the maximum accepted WebSocket message size
func (c *wireConn) SetReadLimit(limit int64) { c.conn.SetReadLimit(limit) }

// Serialize and send a JSON WebSocket message
func (c *wireConn) WriteJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return errors.New("SDK message exceeds 1048576-byte UTF-8 JSON limit")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	// wsjson.Write appends a newline. Keep the exact bytes admitted by Send,
	// including messages at the protocol size boundary
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// Read the next bounded WebSocket message
func (c *wireConn) readMessage() ([]byte, error) {
	kind, data, err := c.conn.Read(c.ctx)
	if err == nil && kind == websocket.MessageText && !utf8.Valid(data) {
		_ = c.Close()
		return nil, errors.New("invalid websocket text")
	}
	return data, err
}
