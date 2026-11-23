// Package sdk implements Stream Dock Manager protocol v1, OpenAction events for
// OpenDeck, and plugin protocol features shared with VSD Craft and Elgato Stream Deck.
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
)

const ContractVersion = 1

// MaxMessageBytes bounds the complete UTF-8 JSON envelope in protocol v1, including legacy hosts
const MaxMessageBytes = 1 << 20
const MaxOutputBytes = 8 * MaxMessageBytes
const MaxLogBytes = MaxMessageBytes

type Surface struct {
	ActivationToken string `json:"activation_token,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	ControlID       string `json:"control_id"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	FeedbackTarget  string `json:"feedback_target"`
	Touch           *struct {
		X     int    `json:"x"`
		Y     int    `json:"y"`
		Phase string `json:"phase"`
	} `json:"touch,omitempty"`
}
type Event struct {
	retained   *incomingLease
	wire       []byte
	Event      string          `json:"event"`
	ID         string          `json:"id,omitempty"`
	Action     string          `json:"action,omitempty"`
	Context    string          `json:"context,omitempty"`
	Device     string          `json:"device,omitempty"`
	UUID       string          `json:"uuid,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Surface    *Surface        `json:"streamDockManager,omitempty"`
	DeviceInfo json.RawMessage `json:"deviceInfo,omitempty"`
	Raw        json.RawMessage `json:"-"`
	ProtocolID string          `json:"protocol_id,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	Method     string          `json:"method,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      *RPCError       `json:"error,omitempty"`
}

// UnmarshalJSON retains the complete event for forward-compatible extensions
func (e *Event) UnmarshalJSON(data []byte) error {
	type wire Event
	var decoded wire
	var envelope = struct {
		*wire
		Error json.RawMessage `json:"error"`
	}{wire: &decoded}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var failure RPCError
		if json.Unmarshal(envelope.Error, &failure) == nil {
			decoded.Error = &failure
		}
	}
	if decoded.Event == "" {
		return errors.New("event name missing")
	}
	if decoded.Event == "didReceiveTelemetry" {
		if err := validateTelemetry(decoded.Payload); err != nil {
			return err
		}
	}
	*e = Event(decoded)
	e.Raw = append(e.Raw[:0], data...)
	return nil
}

type Payload struct {
	Settings    map[string]any  `json:"settings"`
	Ticks       int             `json:"ticks"`
	Hold        bool            `json:"hold"`
	Pressed     bool            `json:"pressed"`
	Controller  string          `json:"controller"`
	State       int             `json:"state"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// Decode the typed payload fields of an event
func (e Event) Data() Payload {
	var p Payload
	_ = json.Unmarshal(e.Payload, &p)
	if p.Settings == nil {
		p.Settings = map[string]any{}
	}
	return p
}

// Construct a host event from a serializable payload
func Message(event, context string, payload any) Event {
	b, _ := json.Marshal(payload)
	return Event{Event: event, Context: context, Payload: b}
}

type Client struct {
	pluginTheme    atomic.Pointer[PluginTheme]
	wireOutput     bool
	queueMu        sync.Mutex
	outputBudget   byteBudget
	logBudget      byteBudget
	logOut         chan Event
	droppedLogs    atomic.Uint64
	logRedactor    func(string) string
	networkDebug   atomic.Bool
	host           *HostInfo
	identity       string
	out            chan Event
	inputMu        sync.Mutex
	inputs         map[string]pendingInput
	requestsMu     sync.Mutex
	requests       map[string]*pendingRequest
	settingsReads  map[string]*pendingRequest
	requestsClosed bool
}

// NewLocalClient supplies the same bounded, capability-gated writer without a
// socket. The caller owns draining the returned events (for simulations/tests)
func NewLocalClient(info HostInfo, identity string) (*Client, <-chan Event) {
	c := &Client{host: &info, identity: identity, out: make(chan Event, 128)}
	return c, c.out
}

type byteBudget struct {
	sizes []int
	bytes int
}

// Reconcile retained outbound bytes after queue consumption
func (b *byteBudget) sync(remaining int) {
	consumed := len(b.sizes) - remaining
	if consumed <= 0 {
		return
	}
	for _, size := range b.sizes[:consumed] {
		b.bytes -= size
	}
	b.sizes = b.sizes[consumed:]
}

// Enqueue a bounded host event after capability checks
func (c *Client) Send(ctx context.Context, e Event) error {
	if e.Event == RPCRequestEvent && !c.Supports(e.ProtocolID) {
		return &UnsupportedFeatureError{e.ProtocolID}
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(encoded) > MaxMessageBytes {
		return errors.New("SDK message exceeds 1048576-byte UTF-8 JSON limit")
	}
	if feature := requiredCapability(e.Event); feature != "" && !c.Supports(feature) {
		return &UnsupportedFeatureError{feature}
	}
	cancel, err := c.trackInput(ctx, e)
	if err != nil {
		return err
	}
	output := c.out
	if e.Event == "logMessage" && c.logOut != nil {
		output = c.logOut
	}
	c.queueMu.Lock()
	budget, limit := &c.outputBudget, MaxOutputBytes
	if e.Event == "logMessage" && c.logOut != nil {
		budget, limit = &c.logBudget, MaxLogBytes
	}
	budget.sync(len(output))
	if budget.bytes+len(encoded) > limit {
		if e.Event == "logMessage" && c.logOut != nil {
			c.droppedLogs.Add(1)
		}
		c.queueMu.Unlock()
		cancel(errors.New("SDK outbound byte budget exceeded"))
		return errors.New("SDK outbound byte budget exceeded")
	}
	// The queue owns the payload snapshot used for both validation and accounting
	if c.wireOutput {
		e = Event{Event: e.Event, wire: encoded}
	} else {
		e.Payload = append(json.RawMessage(nil), e.Payload...)
		e.DeviceInfo = append(json.RawMessage(nil), e.DeviceInfo...)
		if e.Surface != nil {
			surface := *e.Surface
			if surface.Touch != nil {
				touch := *surface.Touch
				surface.Touch = &touch
			}
			e.Surface = &surface
		}
	}
	select {
	case <-ctx.Done():
		c.queueMu.Unlock()
		cancel(ctx.Err())
		return ctx.Err()
	case output <- e:
		budget.sizes = append(budget.sizes, len(encoded))
		budget.bytes += len(encoded)
		c.queueMu.Unlock()
		return nil
	default:
		if e.Event == "logMessage" && c.logOut != nil {
			c.droppedLogs.Add(1)
		}
		c.queueMu.Unlock()
		cancel(errors.New("SDK outbound queue full"))
		return errors.New("SDK outbound queue full")
	}
}

// Send a title update for a binding
func (c *Client) Title(ctx context.Context, binding, text string) error {
	return c.Send(ctx, Message("setTitle", binding, map[string]any{"title": text, "target": 0}))
}

// Send structured feedback to a binding
func (c *Client) Feedback(ctx context.Context, binding string, value any) error {
	return c.Send(ctx, Message("setFeedback", binding, value))
}

// Send an image reference to a binding
func (c *Client) Image(ctx context.Context, binding, image string) error {
	return c.Send(ctx, Message("setImage", binding, map[string]any{"image": image, "target": 0}))
}

type Handler interface {
	Handle(context.Context, *Client, Event) error
	Poll(context.Context, *Client) error
	Close() error
}

// Parse host launch arguments and run the plugin connection
func Run(ctx context.Context, args []string, h Handler) error {
	f := flag.NewFlagSet("plugin", flag.ContinueOnError)
	port := f.Int("port", 0, "loopback host port")
	token := f.String("pluginUUID", "", "authenticated token")
	register := f.String("registerEvent", "registerPlugin", "registration event")
	info := f.String("info", "{}", "host capabilities")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *port < 1 || *port > 65535 || *token == "" || *register != "registerPlugin" {
		return errors.New("invalid authenticated host arguments")
	}
	host, err := ParseHostInfo([]byte(*info))
	if err != nil {
		return err
	}
	networkRunID := ""
	if host.Supports("network-payload-v1") && len(host.Manager.RunID) <= 128 {
		networkRunID = host.Manager.RunID
	}
	return connectWithHost(ctx, "ws://127.0.0.1:"+strconv.Itoa(*port), *token, h, networkRunID, &host)
}

// ConnectWithInfo negotiates optional extensions from explicit host metadata
func ConnectWithInfo(parent context.Context, address, token string, h Handler, info HostInfo) error {
	run := ""
	if info.Supports("network-payload-v1") && len(info.Manager.RunID) <= 128 {
		run = info.Manager.RunID
	}
	return connectWithHost(parent, address, token, h, run, &info)
}

// Own the host connection and coordinate reading, dispatch and shutdown
func connectWithHost(parent context.Context, address, token string, h Handler, networkRunID string, host *HostInfo) (result error) {
	if host == nil {
		return errors.New("host metadata is required")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "ws" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		return errors.New("host must be loopback ws")
	}
	conn, err := dialLoopback(parent, address)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Preserve handler cleanup failures alongside the connection result
	defer func() {
		if closeErr := h.Close(); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	c := &Client{wireOutput: true, out: make(chan Event, 128), logOut: make(chan Event, 128), host: host, identity: token}
	c.SetNetworkDebug(host.Manager.DebugNetwork)
	defer netdebug.Clear(c)
	defer c.cancelInputs("")
	defer c.cancelRequests()
	events := make(chan Event, MaxInputItems)
	var incoming incomingBudget
	readerDone := make(chan struct{})
	failures := make(chan error, 2)
	conn.SetReadLimit(MaxMessageBytes)
	// Register before starting the writer so all writes remain serialized
	capabilities, err := RuntimeCapabilities()
	if err != nil {
		return err
	}
	if capabilities != nil {
		for _, required := range capabilities.RequiredCapabilities {
			if !host.Supports(required) {
				return &UnsupportedFeatureError{required}
			}
		}
	}
	registration := Event{Event: "registerPlugin"}
	if host.Supports("plugin-protocol-v1") {
		payload := map[string]any{"capabilities": capabilities, "network_debug": true}
		if c.networkDemand() {
			payload["network_demand"] = true
		}
		registration = Message("registerPlugin", "", payload)
	}
	registration.UUID = token
	if err = conn.WriteJSON(registration); err != nil {
		return err
	}
	// Route control replies directly so blocking action RPCs do not stall the reader
	go func() {
		defer close(readerDone)
		defer c.cancelRequests()
		for {
			data, err := conn.readMessage()
			var lease *incomingLease
			var e Event
			if err == nil {
				err = json.Unmarshal(data, &e)
				if err == nil && c.ObserveControl(e) {
					continue
				}
			}
			if err == nil {
				lease, err = incoming.acquire(ctx, len(data))
			}
			data = nil
			if err != nil {
				lease.release()
				select {
				case failures <- err:
				case <-ctx.Done():
				}
				return
			}
			e.retained = lease
			select {
			case events <- e:
				if e.Event == "shutdown" {
					return
				}
			case <-ctx.Done():
				lease.release()
				return
			}
		}
	}()
	// Stop the reader before releasing queued envelopes to avoid racing new reservations
	defer func() {
		cancel()
		_ = conn.Close()
		<-readerDone
		for {
			select {
			case event := <-events:
				event.retained.release()
			default:
				return
			}
		}
	}()
	// Keep socket writes serialized while reporting dropped diagnostic messages
	go func() {
		// Forward a write failure to the connection owner without blocking shutdown
		write := func(e Event) bool {
			if err := conn.WriteJSON(e); err != nil {
				select {
				case failures <- err:
				case <-ctx.Done():
				}
				return false
			}
			return true
		}
		for {
			e, ok := c.nextOutbound(ctx)
			if !ok {
				return
			}
			if e.Event == "logMessage" {
				if lost := c.droppedLogs.Swap(0); lost > 0 {
					payload := map[string]any{"message": fmt.Sprintf("SDK log queue overloaded; %d messages were not captured", lost), "incomplete": true}
					if c.Supports("plugin-logs-v1") {
						payload["stream"] = "stderr"
					}
					if !write(Message("logMessage", "", payload)) {
						return
					}
				}
			}
			if !write(e) {
				return
			}
		}
	}()

	if networkRunID != "" && !c.networkDemand() {
		// Report aggregate traffic under a non-secret run identifier until cancellation
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					sent, received := NetworkPayload()
					_ = c.Send(ctx, Message("networkPayload", "", map[string]any{"run_id": networkRunID, "sent_bytes": sent, "received_bytes": received}))
				}
			}
		}()
	}
	if c.SupportsThemeRPC() {
		_, _ = c.GetPluginTheme(ctx)
	}
	var batch dialBatch
	defer func() {
		if batch.pending != nil {
			batch.pending.retained.release()
		}
	}()
	// Release each input reservation after serialized handler dispatch
	deliver := func(e Event) {
		defer e.retained.release()
		if e.Event != ThemeEvent || c.observePluginTheme(e) {
			handleEvent(ctx, c, h, e)
		}
	}
	additive, _ := h.(AdditiveDial)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	var wake <-chan struct{}
	scheduler, adaptive := h.(Scheduler)
	if adaptive {
		wake = scheduler.Wake()
	}

	backoff := time.Duration(0)
	var backoffUntil, pollDeadline time.Time
	// Poll on the owner loop so feature state never races event handling
	poll := func() {
		if err := h.Poll(ctx, c); err != nil {
			if backoff == 0 {
				backoff = time.Second
			} else {
				backoff = min(60*time.Second, backoff*2)
			}
			backoffUntil = time.Now().Add(backoff)
			_ = c.LogStream(ctx, Stderr, fmt.Sprint(err))
		} else {
			backoff = 0
			backoffUntil = time.Time{}
		}
	}
	// Arm the timer from explicit deadlines rather than imposing idle polling
	schedule := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		now := time.Now()
		var deadline time.Time
		if adaptive {
			deadline = scheduler.NextPoll(now)
		}
		if !deadline.IsZero() && backoffUntil.After(deadline) {
			deadline = backoffUntil
		}
		pollDeadline = deadline
		if !batch.deadline.IsZero() && (deadline.IsZero() || batch.deadline.Before(deadline)) {
			deadline = batch.deadline
		}
		if !deadline.IsZero() {
			timer.Reset(max(time.Millisecond, time.Until(deadline)))
		}
	}

	schedule()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case e := <-events:
			if e.Event == "shutdown" {
				e.retained.release()
				batch.flush(deliver)
				return nil
			}
			// Tracked inputs keep their individual completion identity
			batch.accept(e, (e.Surface == nil || e.Surface.RequestID == "") && additive != nil && additive.BatchDial(e.Action), time.Now(), deliver)
			schedule()
		case _, open := <-wake:
			if !open {
				wake = nil
				continue
			}
			poll()
			schedule()
		case <-timer.C:
			now := time.Now()
			if !batch.deadline.IsZero() && !now.Before(batch.deadline) {
				batch.flush(deliver)
			}
			if !pollDeadline.IsZero() && !now.Before(pollDeadline) {
				poll()
			}

			schedule()
		}
	}
}

// Execution/display traffic has priority and independent capacity under a log flood
func (c *Client) nextOutbound(ctx context.Context) (Event, bool) {
	select {
	case e := <-c.out:
		return e, true
	default:
	}
	select {
	case <-ctx.Done():
		return Event{}, false
	case e := <-c.out:
		return e, true
	case e := <-c.logOut:
		return e, true
	}
}
