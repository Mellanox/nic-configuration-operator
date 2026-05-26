// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryUntilSuccess_FirstAttemptSucceeds(t *testing.T) {
	var calls atomic.Int32
	err := retryUntilSuccess(context.Background(), 5, time.Millisecond, func(_ context.Context) error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

func TestRetryUntilSuccess_SucceedsAfterRetries(t *testing.T) {
	var calls atomic.Int32
	err := retryUntilSuccess(context.Background(), 5, time.Millisecond, func(_ context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error after retries, got %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls (2 failures + 1 success), got %d", got)
	}
}

func TestRetryUntilSuccess_FailsAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	sentinel := errors.New("persistent")
	err := retryUntilSuccess(context.Background(), 10, time.Microsecond, func(_ context.Context) error {
		calls.Add(1)
		return sentinel
	})
	if err == nil {
		t.Fatal("expected error after exhausting attempts, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
	if !strings.Contains(err.Error(), "10 attempts") {
		t.Fatalf("expected error message to reference attempt count, got %q", err.Error())
	}
	if got := calls.Load(); got != 10 {
		t.Fatalf("expected exactly 10 calls, got %d", got)
	}
}

func TestRetryUntilSuccess_ContextCancelledDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	sentinel := errors.New("transient")

	// Cancel the context synchronously inside the first attempt; the wait that
	// follows must observe the cancellation and bail out without further calls.
	err := retryUntilSuccess(ctx, 10, time.Hour, func(_ context.Context) error {
		calls.Add(1)
		cancel()
		return sentinel
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly 1 call before cancellation, got %d", got)
	}
}

func TestRetryUntilSuccess_NoWaitAfterFinalAttempt(t *testing.T) {
	// With a single attempt, the function must not wait — the test would hang on
	// the sleep otherwise. We give it a long interval and expect immediate return.
	start := time.Now()
	var calls atomic.Int32
	err := retryUntilSuccess(context.Background(), 1, time.Hour, func(_ context.Context) error {
		calls.Add(1)
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("expected immediate return after final attempt, took %v", elapsed)
	}
}
