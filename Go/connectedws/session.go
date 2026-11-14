// Package connectedws supplies the bounded, tracked transport for external
// service connections. Protocol authentication and command semantics stay in each plugin
package connectedws

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/coder/websocket"
)

const MaxFrame = 4 << 20
const MaxRequest = 64 << 10
const requestTimeout = 8 * time.Second

var ErrClosed = errors.New("WebSocket connection closed; reconnect to refresh state")

// Route validates a protocol envelope, returns a response ID for correlated replies,
// and handles unsolicited events synchronously. It must not block on a request
type Route func(json.RawMessage) (string, error)

// Handshake must use ctx for all authentication reads and writes
type Handshake func(context.Context, *websocket.Conn) error

type Session struct {
	conn    *websocket.Conn
	route   Route
	closed  func()
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	pending map[string]chan json.RawMessage
	writer  chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	pong    *atomic.Pointer[time.Time]
}

// Report whether the service session remains open
func (s *Session) Alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// Close the session and release pending requests
func (s *Session) Close() {
	// Close and notify pending callers once even when heartbeat and reader fail together
	s.once.Do(func() {
		close(s.done)
		s.cancel()
		_ = s.conn.CloseNow()
		if s.closed != nil {
			s.closed()
		}
	})
}

// Send a correlated service request with independent cancellation
func (s *Session) Request(ctx context.Context, id string, value any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if id == "" || len(id) > 128 {
		return nil, fmt.Errorf("invalid WebSocket request ID")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > MaxRequest {
		return nil, fmt.Errorf("invalid or oversized WebSocket request")
	}
	response := make(chan json.RawMessage, 1)
	s.mu.Lock()
	if len(s.pending) >= 32 {
		s.mu.Unlock()
		return nil, fmt.Errorf("too many pending WebSocket requests")
	}
	if _, exists := s.pending[id]; exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("duplicate WebSocket request ID")
	}
	s.pending[id] = response
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()
	select {
	case s.writer <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrClosed
	}
	if !s.Alive() {
		<-s.writer
		return nil, ErrClosed
	}
	if err = ctx.Err(); err != nil {
		<-s.writer
		return nil, err
	}
	// An admitted frame may already be on the wire when the caller cancels
	// A session-owned write deadline protects framing without closing on caller cancellation
	written := make(chan error, 1)
	go s.writeRequest(ctx, data, written)
	select {
	case err := <-written:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrClosed
	}
	select {
	case raw := <-response:
		return raw, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, ErrClosed
	}
}

// Finish one admitted frame under the session deadline and always release the writer slot
func (s *Session) writeRequest(caller context.Context, data []byte, result chan<- error) {
	defer func() { <-s.writer }()
	if err := caller.Err(); err != nil {
		result <- err
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, requestTimeout)
	defer cancel()
	if err := s.conn.Write(ctx, websocket.MessageText, data); err != nil {
		s.Close()
		result <- ErrClosed
		return
	}
	result <- nil
}

// Route service frames to pending requests or unsolicited handlers
func (s *Session) read() {
	defer s.Close()
	for {
		kind, raw, err := s.conn.Read(s.ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText || !json.Valid(raw) {
			return
		}
		id, err := s.route(raw)
		if err != nil {
			return
		}
		if id == "" {
			continue
		}
		s.mu.Lock()
		ch := s.pending[id]
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- raw:
			default:
			}
		}
	}
}

// Maintain session liveness using bounded heartbeat checks
func (s *Session) heartbeat() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			if !s.ping(8 * time.Second) {
				return
			}
		}
	}
}

// Send a heartbeat and await its reply within the timeout
func (s *Session) ping(timeout time.Duration) bool {
	if time.Since(*s.pong.Load()) >= 90*time.Second {
		s.Close()
		return false
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	err := s.conn.Ping(ctx)
	// Ping waits for its matching pong. Preserve the shared 90-second liveness
	// allowance rather than treating an eight-second pong delay as a disconnect
	// A failed/blocked write closes the coder connection and wakes the reader
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		s.Close()
		return false
	}
	return s.Alive()
}

// Client owns at most one authenticated session, including an in-progress dial
// Close is reusable: settings changes and the last unbind cancel outstanding work,
// while a later binding can establish a fresh session. No request is replayed
type Client struct {
	mu         sync.Mutex
	opening    sync.Mutex
	session    *Session
	key        string
	generation uint64
	cancel     context.CancelFunc
}

// Close the client-owned service session
func (c *Client) Close() {
	c.mu.Lock()
	c.generation++
	s, cancel := c.session, c.cancel
	c.session = nil
	c.cancel = nil
	c.key = ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if s != nil {
		s.Close()
	}
}

// Reuse a matching session or establish an authenticated connection
func (c *Client) Connect(ctx context.Context, key, endpoint string, handshake Handshake, route Route, closed func()) (*Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !c.opening.TryLock() {
		return nil, false, fmt.Errorf("WebSocket connection already opening")
	}
	defer c.opening.Unlock()
	c.mu.Lock()
	s := c.session
	valid := c.key == key && s != nil && s.Alive()
	c.mu.Unlock()
	if valid {
		return s, false, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, false, fmt.Errorf("invalid WebSocket URL; use ws:// or wss:// without credentials or query parameters")
	}
	c.Close()
	dialctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	c.mu.Lock()
	generation := c.generation
	c.cancel = cancel
	c.mu.Unlock()
	defer cancel()
	// These server connections are direct. Track above TLS so the SDK's network
	// payload counters exclude TLS negotiation and encrypted record overhead
	transport := &http.Transport{
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, e := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, address)
			return sdk.TrackConn(conn), e
		},
		// Use standard certificate validation before wrapping the TLS connection for accounting
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, e := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}}).DialContext(ctx, network, address)
			return sdk.TrackConn(conn), e
		},
	}
	defer transport.CloseIdleConnections()
	var pong atomic.Pointer[time.Time]
	now := time.Now()
	pong.Store(&now)
	conn, _, err := websocket.Dial(dialctx, u.String(), &websocket.DialOptions{
		HTTPClient:      &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		CompressionMode: websocket.CompressionDisabled,
		OnPongReceived:  func(context.Context, []byte) { now := time.Now(); pong.Store(&now) },
	})
	if err != nil {
		return nil, false, fmt.Errorf("WebSocket connection failed; check the server URL and network")
	}
	conn.SetReadLimit(MaxFrame)
	stop := context.AfterFunc(dialctx, func() { _ = conn.CloseNow() })
	if handshake != nil {
		err = handshake(dialctx, conn)
	}
	stopped := stop()
	if err != nil || !stopped || dialctx.Err() != nil {
		_ = conn.CloseNow()
		if err != nil {
			return nil, false, err
		}
		return nil, false, ErrClosed
	}
	ready := time.Now()
	pong.Store(&ready)
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	s = &Session{conn: conn, route: route, closed: closed, done: make(chan struct{}), pending: map[string]chan json.RawMessage{}, writer: make(chan struct{}, 1), ctx: sessionCtx, cancel: sessionCancel, pong: &pong}
	c.mu.Lock()
	if generation != c.generation || dialctx.Err() != nil {
		c.mu.Unlock()
		s.Close()
		return nil, false, ErrClosed
	}
	c.session = s
	c.key = key
	c.cancel = nil
	c.mu.Unlock()
	go s.read()
	go s.heartbeat()
	return s, true, nil
}

// Rejected distinguishes an authoritative server rejection from an uncertain
// delivery (disconnect/timeout). Callers must never retry either kind of write
type Rejected struct{ Operation string }

// Describe an explicitly rejected service command
func (e *Rejected) Error() string {
	return e.Operation + " was rejected; check permissions and the selected resource"
}

// Distinguish confirmed rejection from an uncertain command outcome
func CommandError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*Rejected](err); ok {
		return err
	}
	return fmt.Errorf("command delivery is uncertain; check the server state before trying again: %w", err)
}
