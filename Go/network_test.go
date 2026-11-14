package sdk

import (
	"io"
	"net"
	"testing"
)

// Verify tracked TCP counts only transferred payload
func TestTrackedTCPCountsOnlyTransferredPayload(t *testing.T) {
	sent0, received0 := NetworkPayload()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	tracked := TrackConn(left)
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(right, "incoming")
		if err == nil {
			data := make([]byte, 3)
			_, err = io.ReadFull(right, data)
		}
		done <- err
	}()
	data := make([]byte, 8)
	if _, err := io.ReadFull(tracked, data); err != nil {
		t.Fatal(err)
	}
	if _, err := tracked.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sent, received := NetworkPayload()
	if sent-sent0 != 3 || received-received0 != 8 {
		t.Fatal("TCP payload accounting mismatch")
	}
}
