package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

type Measurement struct {
	Value      *float64 `json:"value"`
	ObservedAt uint64   `json:"observed_at"`
	MaxAgeMS   uint64   `json:"max_age_ms"`
	Error      *string  `json:"error"`
}

// Check whether a measurement remains fresh at the supplied time
func (m Measurement) Fresh(now time.Time) bool {
	millis := now.UnixMilli()
	return m.Value != nil && m.Error == nil && !math.IsNaN(*m.Value) && !math.IsInf(*m.Value, 0) && millis >= 0 && uint64(millis) >= m.ObservedAt && uint64(millis)-m.ObservedAt <= m.MaxAgeMS
}

type TelemetrySnapshot struct {
	AudioObserver bool                   `json:"audio_observer"`
	CPU           Measurement            `json:"cpu"`
	Memory        Measurement            `json:"memory"`
	Network       Measurement            `json:"network"`
	Disks         map[string]Measurement `json:"disks"`
	AudioVolume   Measurement            `json:"audio_volume"`
	AudioMuted    Measurement            `json:"audio_muted"`
}

// Validate telemetry structure before decoding it
func validateTelemetry(data []byte) error {
	var value struct {
		AudioObserver *bool `json:"audio_observer"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.AudioObserver == nil {
		return fmt.Errorf("telemetry audio_observer must be a boolean")
	}
	return nil
}

// Decode a validated telemetry snapshot
func (s *TelemetrySnapshot) UnmarshalJSON(data []byte) error {
	if err := validateTelemetry(data); err != nil {
		return err
	}
	type wire TelemetrySnapshot
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = TelemetrySnapshot(decoded)
	return nil
}

// Subscribe to the requested host telemetry topics
func (c *Client) SubscribeTelemetry(ctx context.Context, topics []string) error {
	return c.Send(ctx, Message("subscribeTelemetry", "", map[string]any{"topics": topics}))
}
