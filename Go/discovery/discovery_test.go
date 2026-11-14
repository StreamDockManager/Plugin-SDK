package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Verify shared discovery replacement cancellation and deadlines
func TestSharedDiscoveryReplacementCancellationAndDeadlines(t *testing.T) {
	var f struct {
		Discovery struct {
			Context, Channel, Operation, First, Second string
			Now, Timeout                               int64
			InvalidIDs                                 []string `json:"invalid_ids"`
		}
	}
	raw, _ := os.ReadFile("../testdata/helpers-v1.json")
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	s := f.Discovery
	r := Runner{}
	req := Request{Context: s.Context, Channel: s.Channel, Operation: s.Operation, ID: s.First}
	for _, id := range s.InvalidIDs {
		invalid := req
		invalid.ID = id
		if _, err := r.Start(context.Background(), invalid, s.Now, s.Timeout); err == nil {
			t.Fatal("invalid ID accepted", id)
		}
		if _, err := Envelope(invalid, nil, ""); err == nil {
			t.Fatal("uncorrelated response accepted")
		}
	}
	isolated := req
	isolated.Context = "other-context"
	other, err := r.Start(context.Background(), isolated, s.Now, s.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _ := r.Start(context.Background(), req, s.Now, s.Timeout)
	a, _ := r.Start(context.Background(), req, s.Now, s.Timeout)
	if duplicate.Context.Err() == nil || other.Context.Err() != nil {
		t.Fatal("duplicate/context isolation")
	}
	if late, _ := r.Complete(duplicate, nil, "", s.Now); late != nil {
		t.Fatal("duplicate completed late")
	}
	if !r.Cancel(isolated.Context, isolated.Channel, isolated.ID) || other.Context.Err() == nil {
		t.Fatal("isolated cancellation")
	}

	req.ID = s.Second
	b, _ := r.Start(context.Background(), req, s.Now+1, s.Timeout)
	if a.Context.Err() == nil || r.Cancel(s.Context, s.Channel, s.First) {
		t.Fatal("stale cancel")
	}
	if reply, _ := r.Complete(a, nil, "", s.Now+2); reply != nil {
		t.Fatal("stale reply")
	}
	reply, err := r.Complete(b, map[string]any{"request_id": "spoof", "op": "spoof", "devices": []string{"one"}}, "", s.Now+2)
	if err != nil || reply.Payload["request_id"] != s.Second || reply.Payload["op"] != s.Operation || reply.Payload["available"] != true {
		t.Fatal(reply, err)
	}
	b, _ = r.Start(context.Background(), req, s.Now, s.Timeout)
	if r.NextDeadline() != s.Now+s.Timeout {
		t.Fatal("deadline")
	}
	expired, err := r.Expire(s.Now + s.Timeout)
	if err != nil || len(expired) != 1 || expired[0].Payload["error"] != "timeout" || b.Context.Err() == nil {
		t.Fatal(expired, err)
	}
	reply2, err := Envelope(req, map[string]any{"data": strings.Repeat("x", MaxResponseBytes)}, "")
	if err != nil || reply2.Payload["error"] != "response_too_large" {
		t.Fatal(reply2, err)
	}
	for i := range MaxPending {
		req.Context = fmt.Sprint(i)
		if _, err = r.Start(context.Background(), req, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
	req.Context = "overflow"
	if _, err = r.Start(context.Background(), req, 0, 1); err == nil {
		t.Fatal("unbounded queue")
	}
	r.Clear("0")
	if _, err = r.Start(context.Background(), req, 0, 1); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err = r.Start(context.Background(), req, 0, 1); err == nil {
		t.Fatal("closed runner accepted work")
	}
}
