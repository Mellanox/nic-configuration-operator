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
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// retryUntilSuccess calls fn up to maxAttempts times. It returns nil on the first
// successful call. If all attempts fail, it returns the last error wrapped with the
// attempt count. Between attempts it waits the given interval; if the context is
// cancelled during a wait, it returns ctx.Err() immediately without further attempts.
func retryUntilSuccess(ctx context.Context, maxAttempts int, interval time.Duration, fn func(context.Context) error) error {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		log.Log.Error(lastErr, "attempt failed", "attempt", i+1, "of", maxAttempts)
		if i == maxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}
