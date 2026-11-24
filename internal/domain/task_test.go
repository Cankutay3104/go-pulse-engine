package domain_test

import (
	"errors"
	"pulse-engine/internal/domain"
	"testing"
)

func TestNewTask(t *testing.T) {
	// We define every test scenario as a data row in a slice of structs; that's why adding a new edge case only requires appending one block instead of duplicating assertion logic.
	tests := []struct {
		name        string
		id          string
		payload     string
		wantID      string
		wantPayload string
		wantField   string // Empty means we expect NewTask to succeed.
	}{
		{
			name:        "valid task trims whitespace",
			id:          "   task-1   ",
			payload:     "   run   ",
			wantID:      "task-1",
			wantPayload: "run",
		},
		{
			name:      "empty id returns validation error",
			id:        "      ",
			payload:   "run",
			wantField: "id",
		},
		{
			name:      "empty payload returns validation error",
			id:        "task-3",
			payload:   "      ",
			wantField: "payload",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task, err := domain.NewTask(tc.id, tc.payload)

			// Branch 1: Error cases. We handle failure expectations early and return.
			if tc.wantField != "" {
				if err == nil {
					t.Fatalf("expected validation error for field %q, got nil", tc.wantField)
				}
				if task != nil {
					t.Fatalf("expected nil task on error, got %+v", task)
				}

				var valErr *domain.ValidationError
				if !errors.As(err, &valErr) {
					t.Fatalf("expected *domain.ValidationError, got %T (%v)", err, err)
				}
				if valErr.Field != tc.wantField {
					t.Fatalf("got validation field %q, want %q", valErr.Field, tc.wantField)
				}
				// Furthermore, calling Error() verifies the formatting method executes cleanly and counts toward 100% statement coverage.
				if valErr.Error() == "" {
					t.Fatal("expected non-empty validation error message")
				}
				return
			}

			// Branch 2: Happy path. We compare against hardcoded expected literals rather than calling strings.TrimSpace again; the reason why is that mirroring production logic inside a test can hide bugs.
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if task.ID != tc.wantID {
				t.Fatalf("got ID %q, want %q", task.ID, tc.wantID)
			}
			if task.Payload != tc.wantPayload {
				t.Fatalf("got payload %q, want %q", task.Payload, tc.wantPayload)
			}
			if task.Status != domain.StatusPending {
				t.Fatalf("got status %q, want %q", task.Status, domain.StatusPending)
			}
			if task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() {
				t.Fatal("expected non-zero CreatedAt and UpdatedAt timestamps")
			}
		})
	}
}

func TestTask_TransitionTo(t *testing.T) {
	tests := []struct {
		name    string
		from    domain.TaskStatus
		to      domain.TaskStatus
		wantErr bool
	}{
		// Valid state transitions
		{name: "pending to processing", from: domain.StatusPending, to: domain.StatusProcessing, wantErr: false},
		{name: "processing to completed", from: domain.StatusProcessing, to: domain.StatusCompleted, wantErr: false},
		{name: "processing to failed", from: domain.StatusProcessing, to: domain.StatusFailed, wantErr: false},

		// Illegal transitions (including terminal and uninitialized states)
		{name: "pending to completed is illegal", from: domain.StatusPending, to: domain.StatusCompleted, wantErr: true},
		{name: "completed to pending is illegal", from: domain.StatusCompleted, to: domain.StatusPending, wantErr: true},
		{name: "failed to processing is illegal", from: domain.StatusFailed, to: domain.StatusProcessing, wantErr: true},
		{name: "unknown empty state fails closed", from: "", to: domain.StatusPending, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &domain.Task{
				ID:      "task-1",
				Payload: "run",
				Status:  tc.from,
			}

			err := task.TransitionTo(tc.to)

			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalidTransition) {
					t.Fatalf("got error %v, want %v", err, domain.ErrInvalidTransition)
				}
				// Moreover, a rejected transition must never partially mutate the struct; that's why we assert the status remains unchanged.
				if task.Status != tc.from {
					t.Fatalf("status mutated on error: got %q, want %q", task.Status, tc.from)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if task.Status != tc.to {
				t.Fatalf("got status %q, want %q", task.Status, tc.to)
			}
			if task.UpdatedAt.IsZero() {
				t.Fatal("expected UpdatedAt to be set after valid transition")
			}
		})
	}
}
