package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Encode a raw WebSocket frame for transport tests
func testFrame(op byte, final bool, data []byte) []byte {
	first := op
	if final {
		first |= 128
	}
	out := []byte{first, byte(len(data))}
	if len(data) >= 126 && len(data) <= 65535 {
		out = []byte{first, 126, byte(len(data) >> 8), byte(len(data))}
	}
	if len(data) > 65535 {
		out = make([]byte, 10)
		out[0] = first
		out[1] = 127
		binary.BigEndian.PutUint64(out[2:], uint64(len(data)))
	}
	return append(out, data...)
}

// A raw peer keeps these tests independent of the client's WebSocket library
func rawHost(t *testing.T, handle func(net.Conn, *bufio.Reader)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		digest := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(digest[:]))
		if err = rw.Flush(); err != nil {
			t.Error(err)
			return
		}
		handle(conn, rw.Reader)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// Decode one masked client WebSocket frame
func readClientFrame(r io.Reader) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	if header[1]&128 == 0 {
		return 0, nil, fmt.Errorf("unmasked client message")
	}
	size := uint64(header[1] & 127)
	if size == 126 {
		var n [2]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(n[:]))
	}
	if size == 127 {
		var n [8]byte
		if _, err := io.ReadFull(r, n[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(n[:])
	}
	if size > MaxMessageBytes {
		return 0, nil, fmt.Errorf("unbounded client message")
	}
	var mask [4]byte
	if _, err := io.ReadFull(r, mask[:]); err != nil {
		return 0, nil, err
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	for i := range data {
		data[i] ^= mask[i%4]
	}
	return header[0] & 15, data, nil
}

// Verify wire fragments controls and masking
func TestWireFragmentsControlsAndMasking(t *testing.T) {
	done := make(chan error, 1)
	endpoint := rawHost(t, func(conn net.Conn, r *bufio.Reader) {
		input := append(testFrame(1, false, []byte(`{"event":`)), testFrame(9, true, []byte("ping"))...)
		input = append(input, testFrame(0, true, []byte(`"shutdown"}`))...)
		if _, err := conn.Write(input); err != nil {
			done <- err
			return
		}
		op, data, err := readClientFrame(r)
		if err != nil || op != 10 || string(data) != "ping" {
			done <- fmt.Errorf("pong: %d %q %v", op, data, err)
			return
		}
		op, data, err = readClientFrame(r)
		if err != nil || op != 1 || !json.Valid(data) {
			done <- fmt.Errorf("text: %d %q %v", op, data, err)
			return
		}
		done <- nil
	})
	conn, err := dialLoopback(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.readMessage()
	if err != nil || string(raw) != `{"event":"shutdown"}` {
		t.Fatal(string(raw), err)
	}
	if err = conn.WriteJSON(Event{Event: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

// Verify wire rejects invalid frames and limits
func TestWireRejectsInvalidFramesAndLimits(t *testing.T) {
	for i, data := range [][]byte{
		{0xc1, 0}, {0x81, 0x80}, {0x83, 0}, {0x80, 0}, {0x09, 0},
		testFrame(1, true, []byte{0xff}), testFrame(8, true, []byte{0}),
		append(testFrame(1, false, []byte("abc")), testFrame(1, true, []byte("def"))...),
		append(testFrame(1, false, []byte("abc")), testFrame(0, true, []byte("def"))...),
		testFrame(2, true, bytes.Repeat([]byte{1}, 126)),
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			endpoint := rawHost(t, func(conn net.Conn, _ *bufio.Reader) { _, _ = conn.Write(data) })
			conn, err := dialLoopback(context.Background(), endpoint)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetReadLimit(5)
			if _, err = conn.readMessage(); err == nil {
				t.Fatal("accepted invalid or oversized frame")
			}
		})
	}
}

// Verify wire extended lengths
func TestWireExtendedLengths(t *testing.T) {
	for _, length := range []int{125, 126, 65535, 65536} {
		payload := bytes.Repeat([]byte("x"), length)
		endpoint := rawHost(t, func(conn net.Conn, _ *bufio.Reader) { _, _ = conn.Write(testFrame(2, true, payload)) })
		conn, err := dialLoopback(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		got, err := conn.readMessage()
		_ = conn.Close()
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatal(length, err)
		}
	}
}

// Verify loopback handshake cancellation
func TestLoopbackHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = dialLoopback(ctx, "ws://"+listener.Addr().String()); err == nil {
		t.Fatal("stalled upgrade ignored cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled handshake leaked connection")
	}
	for _, address := range []string{"wss://localhost:80", "ws://example.com", "ws://user@localhost", "ws://127.0.0.1:0", "ws://127.0.0.1/#fragment"} {
		if _, err := dialLoopback(context.Background(), address); err == nil {
			t.Fatal("accepted unsupported address", address)
		}
	}
}

// Verify loopback rejects redirects and oversized upgrade
func TestLoopbackRejectsRedirectsAndOversizedUpgrade(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer target.Close()
	for _, kind := range []string{"redirect", "headers"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if kind == "redirect" {
				http.Redirect(w, r, target.URL, http.StatusFound)
				return
			}
			w.Header().Set("X-Oversized", strings.Repeat("x", 2*maxUpgradeBytes))
			w.WriteHeader(http.StatusSwitchingProtocols)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := dialLoopback(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
		cancel()
		server.Close()
		if err == nil {
			t.Fatal("accepted", kind)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("followed host redirect")
	}
}

// Verify wire message boundary does not add unaccounted bytes
func TestWireMessageBoundaryDoesNotAddUnaccountedBytes(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		received := make(chan []byte, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			conn.SetReadLimit(MaxMessageBytes)
			_, data, _ := conn.Read(context.Background())
			received <- data
		}))
		conn, err := dialLoopback(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http"))
		if err != nil {
			t.Fatal(err)
		}
		value := map[string]any{"event": "setTitle", "payload": map[string]any{"title": "é"}}
		encoded, _ := json.Marshal(value)
		value["payload"] = map[string]any{"title": "é" + strings.Repeat("x", MaxMessageBytes-len(encoded)+delta)}
		err = conn.WriteJSON(value)
		_ = conn.Close()
		data := <-received
		server.Close()
		if delta > 0 {
			if err == nil || len(data) != 0 {
				t.Fatal("oversized message wrote bytes", err)
			}
			continue
		}
		if err != nil || len(data) != MaxMessageBytes+delta {
			t.Fatal("wire bytes differ from admission accounting", len(data), err)
		}
	}
}
