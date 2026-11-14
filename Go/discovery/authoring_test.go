package discovery

import (
	"context"
	"encoding/json"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"path/filepath"
	"testing"
	"time"
)

// Verify runner isolates commands and reports uncooperative read timeout
func TestRunnerIsolatesCommandsAndReportsUncooperativeReadTimeout(t *testing.T) {
	e := authoring.New(authoring.Definition{}, filepath.Join(t.TempDir(), "state.json"), func(*authoring.Engine) authoring.Feature { return authoring.Feature{} })
	defer e.Close()
	release := make(chan struct{})
	defer close(release)
	commandStarted := make(chan context.Context, 1)
	if !e.StartJob(context.Background(), "control", func(ctx context.Context) (string, any, error) { commandStarted <- ctx; <-release; return "", nil, nil }, nil) {
		t.Fatal("command not admitted")
	}
	command := <-commandStarted
	client, events := sdk.NewLocalClient(sdk.HostInfo{}, "fixture")
	r := Runner{}
	request := Request{Context: "control", Channel: "devices", ID: "old", Operation: "devices"}
	work := func(context.Context) (map[string]any, error) {
		<-release
		return map[string]any{"devices": []string{"late"}}, nil
	}
	if err := r.Run(context.Background(), e, client, request, time.Second, work); err != nil {
		t.Fatal(err)
	}
	if !r.Cancel("control", "devices", "old") {
		t.Fatal("read did not cancel")
	}
	request.ID = "new"
	if err := r.Run(context.Background(), e, client, request, 20*time.Millisecond, work); err != nil {
		t.Fatal(err)
	}
	if r.Cancel("control", "devices", "old") {
		t.Fatal("stale cancel accepted")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := e.DrainJobs(context.Background(), client); err != nil {
			t.Fatal(err)
		}
		select {
		case event := <-events:
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["request_id"] != "new" || payload["error"] != "timeout" {
				t.Fatal(event)
			}
			if _, ok := e.Jobs["control"]; !ok || command.Err() != nil {
				t.Fatal("discovery interfered with command")
			}
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("uncooperative discovery prevented deadline reply")
}
