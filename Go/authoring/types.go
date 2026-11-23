// Package authoring owns the shared plugin lifecycle; it imports no feature implementation
package authoring

import (
	"context"
	"sync"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/storage"
)

type Definition struct {
	ID string `json:"id,omitempty"`
	// RuntimeAssets lists feature-owned paths selected dynamically rather than by action metadata
	RuntimeAssets        []string     `json:"-"`
	ViewTargets          []ViewTarget `json:"-"`
	Slug                 string       `json:"slug"`
	Name                 string       `json:"name"`
	Version              string       `json:"version"`
	MinimumManager       string       `json:"minimum_manager"`
	RequiredCapabilities []string     `json:"required_capabilities"`
	Actions              []string     `json:"actions"`
}
type Binding struct {
	Action   string
	Settings map[string]any
	Surface  *sdk.Surface
}

// Presentation is optional semantic content. Text remains the legacy/customized
// live-text source; zero content preserves historical rendering
type Presentation struct {
	Kind         string
	Primary      string
	Label        string
	Detail       string
	Progress     float64
	ShowProgress bool
}
type DisplayStatus struct {
	Content     *Presentation
	Icon        string
	Text        string
	State       int
	Unavailable bool
}
type Display struct {
	Layout   string
	Image    string
	Title    string
	State    int
	Feedback map[string]any
}
type Visual struct {
	Icon   string
	States []string
}
type SampledStatus struct {
	Status   DisplayStatus
	At       time.Time
	Failures int
	Next     time.Time
}

// Feature callbacks run on the SDK event loop. Background work receives immutable snapshots
type Feature struct {
	// ObserveFailure is an optional nonblocking application policy hook for admitted failures
	ObserveFailure func(context.Context, *sdk.Client, string, error)
	// FilterStatus applies optional application display policy before status deduplication
	FilterStatus    func(context.Context, *sdk.Client, string, DisplayStatus) DisplayStatus
	ShouldLogAction func(sdk.Event) bool
	DemandChanged   func()
	Present         func(string, DisplayStatus) Presentation
	Actions         map[string]func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Before          func(context.Context, *sdk.Client, sdk.Event) (bool, error)
	After           func(context.Context, *sdk.Client, sdk.Event) error
	Action          func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Inspector       func(context.Context, *sdk.Client, sdk.Event) (bool, error)
	Settings        func(string)
	Clear           func(string)
	Unbound         func(string)
	Status          func(context.Context, string, Binding) DisplayStatus
	Collect         func(context.Context, string, Binding) DisplayStatus
	Poll            func(context.Context, *sdk.Client) (bool, error)
	Deadline        func(time.Time) time.Time
	View            func(string, DisplayStatus) Display
	Render          func(context.Context, *sdk.Client, string) error
	Close           func()
	Progress        func()
	Snapshot        func() any
	Apply           func(context.Context, *sdk.Client, JobResult, BackgroundJob) error
	Visuals         map[string]Visual
	DynamicDisplay  bool
	AdditiveDial    func(string) bool
}
type Factory func(*Engine) Feature
type ActionHooks struct {
	Input       func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Appeared    func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Changed     func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Disappeared func(context.Context, *sdk.Client, sdk.Event, Binding) error
	Render      func(context.Context, *sdk.Client, string, Binding) (Display, error)
}
type Engine struct {
	secretFields     map[string]map[string]any
	redaction        jobRedaction
	stateReadBlocked bool
	Now              func() time.Time
	Registered       map[string]ActionHooks
	Store            *storage.Store

	Slug, Path, Theme          string
	Global                     map[string]any
	Bindings                   map[string]Binding
	Statuses, Samples          map[string]SampledStatus
	Probes                     *sdk.ProbePool
	WakeChannel                chan struct{}
	PendingWrite, NextPeriodic time.Time
	Written                    []byte
	DirtyState, Closed         bool
	ProbeKeys                  map[string]string
	Effective                  map[string]map[string]any
	DisplayTicks               map[string]int
	Jobs                       map[string]BackgroundJob
	Generation                 uint64
	Completed                  chan JobResult
	ResultsText                map[string]string
	Results                    map[string]any
	Checked                    map[string]time.Time
	Last                       map[string]Display
	LastStatus                 map[string]DisplayStatus
	Mu                         sync.Mutex
	Feature                    Feature
	ViewTargets                map[string]ViewTarget
	ViewActive                 map[string]bool
}
type BackgroundJob struct {
	Cancel     context.CancelFunc
	StopCancel func() bool
	Generation uint64
	Complete   func(error)
	Owner      string
	Refresh    bool
	ActionCtx  context.Context
	Poll       any
	Apply      func(context.Context, *sdk.Client, JobResult, BackgroundJob) error
}
type JobResult struct {
	ID         string
	Generation uint64
	Text       string
	Value      any
	Err        error
}
