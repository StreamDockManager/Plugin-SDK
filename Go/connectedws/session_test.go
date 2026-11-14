package connectedws

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Start a local WebSocket service with the supplied frame handler
func fixture(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		handle(conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// Extract the request identifier from a service fixture reply
func testRoute(raw json.RawMessage) (string, error) {
	var r struct {
		ID string `json:"id"`
	}
	err := json.Unmarshal(raw, &r)
	return r.ID, err
}

// Verify correlated out of order replies and cancellation
func TestCorrelatedOutOfOrderRepliesAndCancellation(t *testing.T) {
	got := make(chan struct{})
	writeLate := make(chan struct{})
	var lateOnce sync.Once
	allowLate := func() { lateOnce.Do(func() { close(writeLate) }) }
	defer allowLate()
	endpoint := fixture(t, func(conn *websocket.Conn) {
		var first, second map[string]any
		if wsjson.Read(context.Background(), conn, &first) != nil || wsjson.Read(context.Background(), conn, &second) != nil {
			return
		}
		_ = wsjson.Write(context.Background(), conn, second)
		_ = wsjson.Write(context.Background(), conn, first)
		var pending map[string]any
		if wsjson.Read(context.Background(), conn, &pending) != nil {
			return
		}
		close(got)
		<-writeLate
		// A late reply must not disrupt the next request on the same socket
		_ = wsjson.Write(context.Background(), conn, pending)
		var next map[string]any
		if wsjson.Read(context.Background(), conn, &next) == nil {
			_ = wsjson.Write(context.Background(), conn, next)
		}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	var client Client
	defer client.Close()
	session, _, err := client.Connect(context.Background(), "fixture", endpoint, func(context.Context, *websocket.Conn) error { return nil }, testRoute, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, _, err := client.Connect(cancelled, "fixture", endpoint, nil, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reuse accepted", err)
	}
	if _, _, err := client.Connect(context.Background(), "invalid", "https://invalid", nil, nil, nil); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	if !session.Alive() {
		t.Fatal("invalid connection request closed active session")
	}
	answers := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := session.Request(ctx, id, map[string]any{"id": id})
			if err == nil && !strings.Contains(string(raw), id) {
				err = errors.New("wrong response delivered")
			}
			answers <- err
		}(id)
	}
	for range 2 {
		if err := <-answers; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := session.Request(ctx, "three", map[string]any{"id": "three"}); done <- err }()
	<-got
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not stop")
	}
	session.mu.Lock()
	count := len(session.pending)
	session.mu.Unlock()
	if count != 0 {
		t.Fatal("pending reply leaked", count)
	}
	if !session.Alive() {
		t.Fatal("reply cancellation closed shared session")
	}
	allowLate()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := session.Request(ctx, "four", map[string]string{"id": "four"})
	if err != nil || !strings.Contains(string(raw), "four") {
		t.Fatal("follow-up request failed after cancellation", err)
	}
}

// Hold a successful socket write open while its caller cancels
type heldWriteConn struct {
	net.Conn
	hold    *atomic.Bool
	written chan<- struct{}
	resume  <-chan struct{}
}

// Pause after the bytes leave the socket but before the WebSocket write returns
func (c *heldWriteConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil && c.hold.CompareAndSwap(true, false) {
		close(c.written)
		<-c.resume
	}
	return n, err
}

// Verify cancellation while the socket write is returning does not close the session
func TestCancelAfterFrameAlreadyReachedSocketKeepsSession(t *testing.T) {
	endpoint := fixture(t, func(conn *websocket.Conn) {
		for {
			var frame map[string]string
			if wsjson.Read(context.Background(), conn, &frame) != nil {
				return
			}
			if frame["id"] == "follow-up" {
				_ = wsjson.Write(context.Background(), conn, frame)
			}
		}
	})
	written, resume := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(resume) }) }
	var hold atomic.Bool
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &heldWriteConn{Conn: conn, hold: &hold, written: written, resume: resume}, nil
	}}
	conn, _, err := websocket.Dial(context.Background(), endpoint, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	session := &Session{conn: conn, ctx: sessionCtx, cancel: sessionCancel, route: testRoute,
		done: make(chan struct{}), pending: map[string]chan json.RawMessage{}, writer: make(chan struct{}, 1)}
	defer session.Close()
	defer release()
	go session.read()
	hold.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() {
		_, err := session.Request(ctx, "cancel", map[string]string{"id": "cancel"})
		canceled <- err
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("socket write did not reach its return boundary")
	}
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("request did not report caller cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation waited for the socket write to return")
	}
	release()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	raw, err := session.Request(ctx, "follow-up", map[string]string{"id": "follow-up"})
	if err != nil || !strings.Contains(string(raw), "follow-up") {
		t.Fatal("connection did not survive cancellation at write completion", err)
	}
}

// Verify bounded frames and requests
func TestBoundedFramesAndRequests(t *testing.T) {
	endpoint := fixture(t, func(conn *websocket.Conn) {
		var request any
		if wsjson.Read(context.Background(), conn, &request) != nil {
			return
		}
		_ = conn.Write(context.Background(), websocket.MessageText, []byte(`{"oversized":"`+strings.Repeat("x", MaxFrame)+`"}`))
	})
	var client Client
	defer client.Close()
	session, _, err := client.Connect(context.Background(), "fixture", endpoint, func(context.Context, *websocket.Conn) error { return nil }, testRoute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Request(context.Background(), "too-big", map[string]any{"id": strings.Repeat("x", MaxRequest)}); err == nil {
		t.Fatal("unbounded outgoing message")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = session.Request(ctx, "small", map[string]any{"id": "small"}); !errors.Is(err, ErrClosed) {
		t.Fatal("oversized response accepted", err)
	}
}

// Verify close cancels authentication and can reconnect
func TestCloseCancelsAuthenticationAndCanReconnect(t *testing.T) {
	entered := make(chan struct{}, 2)
	var connections atomic.Int32
	endpoint := fixture(t, func(conn *websocket.Conn) {
		connections.Add(1)
		entered <- struct{}{}
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	})
	var client Client
	defer client.Close()
	finished := make(chan error, 1)
	go func() {
		_, _, err := client.Connect(context.Background(), "fixture", endpoint, func(ctx context.Context, conn *websocket.Conn) error {
			var greeting any
			return wsjson.Read(ctx, conn, &greeting)
		}, testRoute, nil)
		finished <- err
	}()
	<-entered
	client.Close()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("closed handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel handshake")
	}
	session, fresh, err := client.Connect(context.Background(), "fixture", endpoint, func(context.Context, *websocket.Conn) error { return nil }, testRoute, nil)
	if err != nil || !fresh || session == nil {
		t.Fatal("reconnect", err, fresh)
	}
	// The upgrade response can reach the client before the server handler has
	// incremented its counter. Observe server acceptance before asserting totals
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reconnected server handler did not start")
	}
	again, fresh, err := client.Connect(context.Background(), "fixture", endpoint, nil, testRoute, nil)
	if err != nil || fresh || again != session || connections.Load() != 2 {
		t.Fatal("connection was not shared", err, fresh, connections.Load())
	}
	client.Close()
	if session.Alive() {
		t.Fatal("closed connection remains live")
	}
}

// Verify untrusted TLS fails before authentication
func TestUntrustedTLSFailsBeforeAuthentication(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	var client Client
	defer client.Close()
	authenticated := false
	_, _, err := client.Connect(context.Background(), "tls", "wss"+strings.TrimPrefix(server.URL, "https"), func(context.Context, *websocket.Conn) error { authenticated = true; return nil }, testRoute, nil)
	if err == nil || authenticated || requests.Load() != 0 {
		t.Fatal("untrusted TLS reached authentication", err, authenticated, requests.Load())
	}
}

// Verify queued write cancellation keeps session
func TestQueuedWriteCancellationKeepsSession(t *testing.T) {
	endpoint := fixture(t, func(conn *websocket.Conn) { _, _, _ = conn.Read(context.Background()) })
	var client Client
	defer client.Close()
	session, _, err := client.Connect(context.Background(), "queue", endpoint, nil, testRoute, nil)
	if err != nil {
		t.Fatal(err)
	}
	session.writer <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = session.Request(ctx, "cancelled", map[string]string{"id": "cancelled"})
	<-session.writer
	if !errors.Is(err, context.DeadlineExceeded) || !session.Alive() {
		t.Fatal("queued cancellation closed shared connection", err)
	}
	session.mu.Lock()
	count := len(session.pending)
	session.mu.Unlock()
	if count != 0 {
		t.Fatal("cancelled request retained a pending slot")
	}
}

// Verify heartbeat keeps pong allowance and closes once
func TestHeartbeatKeepsPongAllowanceAndClosesOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: func(context.Context, []byte) bool { return false }})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(context.Background())
	}))
	defer server.Close()
	var client Client
	defer client.Close()
	var closed atomic.Int32
	session, _, err := client.Connect(context.Background(), "heartbeat", "ws"+strings.TrimPrefix(server.URL, "http"), nil, testRoute, func() { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if !session.ping(25*time.Millisecond) || !session.Alive() {
		t.Fatal("missing immediate pong closed connection before liveness deadline")
	}
	expired := time.Now().Add(-91 * time.Second)
	session.pong.Store(&expired)
	if session.ping(time.Second) || session.Alive() {
		t.Fatal("missing pongs did not expire session")
	}
	session.Close()
	if closed.Load() != 1 {
		t.Fatal("close callback was not exactly once")
	}
}

// Verify service redirect is rejected before authentication
func TestServiceRedirectIsRejectedBeforeAuthentication(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	var client Client
	defer client.Close()
	authenticated := false
	_, _, err := client.Connect(context.Background(), "redirect", "ws"+strings.TrimPrefix(server.URL, "http"), func(context.Context, *websocket.Conn) error { authenticated = true; return nil }, testRoute, nil)
	if err == nil || authenticated || reached.Load() != 0 {
		t.Fatal("redirect reached authentication", err, authenticated, reached.Load())
	}
}
