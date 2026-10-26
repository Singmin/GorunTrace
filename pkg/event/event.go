// Package event defines the public wire format shared by SDK and collectors.
package event

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

const (
	Started   = "started"
	Succeeded = "succeeded"
	Failed    = "failed"
	TimedOut  = "timed_out"
	Canceled  = "canceled"
	Panicked  = "panicked"
)

type Event struct {
	EventID      string    `json:"event_id"`
	RunID        string    `json:"run_id"`
	Service      string    `json:"service"`
	TaskName     string    `json:"task_name"`
	Type         string    `json:"type"`
	Attempt      int       `json:"attempt"`
	StartedAt    time.Time `json:"started_at"`
	OccurredAt   time.Time `json:"occurred_at"`
	DurationMS   float64   `json:"duration_ms"`
	ErrorSummary string    `json:"error_summary,omitempty"`
}

type Batch struct {
	Events []Event `json:"events"`
}

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("goruntrace: random ID: %w", err))
	}
	return hex.EncodeToString(b[:])
}

func ValidStatus(s string) bool {
	switch s {
	case Started, Succeeded, Failed, TimedOut, Canceled, Panicked:
		return true
	}
	return false
}

func (e Event) Validate() error {
	for _, v := range []string{e.EventID, e.RunID, e.Service, e.TaskName} {
		if !ValidName(v) {
			return errors.New("IDs, service and task_name must contain 1–200 bytes")
		}
	}
	if !ValidStatus(e.Type) {
		return errors.New("invalid event type")
	}
	if e.Attempt < 1 || e.Attempt > 1000000 {
		return errors.New("attempt must be between 1 and 1000000")
	}
	if e.StartedAt.IsZero() || e.OccurredAt.IsZero() || e.OccurredAt.Before(e.StartedAt) {
		return errors.New("invalid event timestamps")
	}
	if e.OccurredAt.After(time.Now().Add(5 * time.Minute)) {
		return errors.New("event timestamp too far in future")
	}
	if e.DurationMS < 0 || math.IsNaN(e.DurationMS) || math.IsInf(e.DurationMS, 0) || len(e.ErrorSummary) > 2048 {
		return errors.New("invalid duration or error summary")
	}
	if e.Type == Started && (!e.StartedAt.Equal(e.OccurredAt) || e.DurationMS != 0 || e.ErrorSummary != "") {
		return errors.New("started event must match start time and contain no result")
	}
	return nil
}

// ValidName rejects empty, oversized and control-character-containing identities.
func ValidName(v string) bool {
	return strings.TrimSpace(v) != "" && len(v) <= 200 && !strings.ContainsFunc(v, unicode.IsControl)
}
