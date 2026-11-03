// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package firmwareimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var (
	ErrPending    = errors.New("firmware image acquisition is pending")
	ErrCapacity   = errors.New("firmware image cache capacity exceeded")
	ErrNotDesired = errors.New("firmware image is not in the desired inventory")
	ErrClosed     = errors.New("firmware image store is closed")
)

// FetchFunc writes the verified payload as destinationDir/firmware.bin. The store
// provides a private directory; FetchFunc must honor cancellation and its declared
// staging-byte bound, remove other temporary files, and never publish elsewhere.
type FetchFunc func(ctx context.Context, reference, destinationDir string) error

// StoreOptions bounds acquisition work and disk consumption, including staging.
type StoreOptions struct {
	Workers               int
	MaxImages             int
	MaxCacheBytes         int64
	MaxPayloadBytes       int64
	FetchReservationBytes int64
	AttemptTimeout        time.Duration
	RetryInitial          time.Duration
	RetryMax              time.Duration
}

func DefaultStoreOptions() StoreOptions {
	return StoreOptions{
		Workers: 2, MaxImages: 128, MaxCacheBytes: 4 << 30, MaxPayloadBytes: 512 << 20,
		FetchReservationBytes: (520 + 512 + 1) << 20,
		AttemptTimeout:        2 * time.Minute, RetryInitial: time.Second, RetryMax: 30 * time.Second,
	}
}

type storeEntry struct {
	reference string
	key       string
	desired   bool
	path      string
	size      int64
	pins      int
	fetching  bool
	cancel    context.CancelFunc
	err       error
	next      time.Time
	backoff   time.Duration
	order     uint64
}

// Store owns an ephemeral immutable cache. Acquisition never performs network I/O
// on the caller's goroutine. Every acquired pathname remains present until its
// release function is called, even after removal from the desired inventory.
type Store struct {
	mu        sync.Mutex
	root      string
	owner     *os.File
	fetch     FetchFunc
	options   StoreOptions
	entries   map[string]*storeEntry
	orphans   map[string]int64 // failed staging cleanups retain their byte reservations
	used      int64            // published bytes plus worst-case reservations for active fetches
	sequence  uint64
	overflow  bool
	gcErr     error
	started   bool
	closed    bool
	workers   int
	cancel    context.CancelFunc
	wake      chan struct{}
	changes   chan struct{}
	removeAll func(string) error
}

// NewStore acquires exclusive ownership of root/store-v1. Previously owned cache
// contents are discarded before any path can be acquired; unrelated root files
// are preserved. Call Run or Close to release ownership.
func NewStore(root string, fetch FetchFunc, options StoreOptions) (*Store, error) {
	if fetch == nil || options.Workers < 1 || options.MaxImages < 1 || options.MaxPayloadBytes < 1 ||
		options.FetchReservationBytes < options.MaxPayloadBytes || options.MaxCacheBytes < options.FetchReservationBytes ||
		options.AttemptTimeout <= 0 || options.RetryInitial <= 0 || options.RetryMax < options.RetryInitial {
		return nil, errors.New("invalid firmware image store configuration")
	}
	cacheRoot, owner, err := prepareStoreRoot(root)
	if err != nil {
		return nil, err
	}
	return &Store{
		root: cacheRoot, owner: owner, fetch: fetch, options: options,
		entries: make(map[string]*storeEntry), orphans: make(map[string]int64),
		wake: make(chan struct{}, options.Workers), changes: make(chan struct{}, 1),
		removeAll: os.RemoveAll,
	}, nil
}

// SetDesired changes node-local acquisition demand. Existing requested entries
// retain priority over new ones. Excess references return ErrCapacity from
// Acquire instead of preventing already available devices from proceeding.
func (s *Store) SetDesired(references []string) error {
	refs := slices.Clone(references)
	slices.Sort(refs)
	refs = slices.Compact(refs)
	wanted := make(map[string]string, len(refs))
	for _, ref := range refs {
		key, err := ImageKey(ref)
		if err != nil {
			return err
		}
		wanted[ref] = key
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.cleanOrphansLocked()
	for _, entry := range s.entries {
		_, entry.desired = wanted[entry.reference]
		if !entry.desired {
			if entry.cancel != nil {
				entry.cancel()
			}
			s.pruneLocked(entry)
		}
	}
	s.overflow = false
	for _, ref := range refs {
		if _, exists := s.entries[ref]; exists {
			continue
		}
		if len(s.entries) == s.options.MaxImages {
			s.overflow = true
			continue
		}
		s.entries[ref] = &storeEntry{reference: ref, key: wanted[ref], desired: true, order: s.nextOrderLocked()}
	}
	s.signalWorkersLocked()
	return nil
}

// Acquire returns immediately. ErrPending means a worker will retry or is still
// fetching. A successful caller must release the pin after all path-based tools
// have returned. The release function is safe to call more than once.
func (s *Store) Acquire(reference string) (string, func(), error) {
	if _, err := ImageKey(reference); err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", nil, ErrClosed
	}
	entry := s.entries[reference]
	if entry == nil {
		if s.overflow {
			return "", nil, ErrCapacity
		}
		return "", nil, ErrNotDesired
	}
	if !entry.desired {
		return "", nil, ErrNotDesired
	}
	if entry.path == "" {
		if entry.err != nil {
			return "", nil, entry.err
		}
		s.signalWorkersLocked()
		return "", nil, ErrPending
	}
	entry.pins++
	var once sync.Once
	release := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			entry.pins--
			if s.pruneLocked(entry) {
				s.signalWorkersLocked()
				s.notifyLocked()
			}
			s.closeOwnerLocked()
		})
	}
	return entry.path, release, nil
}

// Changes coalesces changes in acquisition state into node reconciliation events.
func (s *Store) Changes() <-chan struct{} { return s.changes }

// Run starts the bounded workers once and stops cleanly when ctx is canceled.
// Ownership is retained after shutdown until all previously acquired pins end.
func (s *Store) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("firmware image store context is required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.started {
		s.mu.Unlock()
		return errors.New("firmware image store is already running")
	}
	workerCtx, cancel := context.WithCancel(ctx)
	s.started, s.cancel, s.workers = true, cancel, s.options.Workers
	s.mu.Unlock()
	defer func() { _ = s.Close() }()
	var workers sync.WaitGroup
	for range s.options.Workers {
		workers.Go(func() { s.worker(workerCtx) })
	}
	workers.Wait()
	return nil
}

// Close is idempotent. Active fetches are canceled; existing acquired paths remain
// available and the ownership lock is kept until workers and pins are released.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.closeOwnerLocked()
	return nil
}

type fetchJob struct {
	entry  *storeEntry
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *Store) worker(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.workers--
		s.closeOwnerLocked()
	}()
	ticker := time.NewTicker(min(s.options.RetryInitial, time.Second))
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job := s.nextJob(ctx)
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			case <-ticker.C:
			}
			continue
		}
		stage, err := os.MkdirTemp(s.root, ".staging-")
		if err == nil {
			err = s.fetch(job.ctx, job.entry.reference, stage)
		}
		var payloadBytes int64
		if err == nil {
			payloadBytes, err = s.checkPayload(stage)
		}
		s.finishJob(job, stage, payloadBytes, err)
		job.cancel()
	}
}

func (s *Store) nextJob(ctx context.Context) *fetchJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || ctx.Err() != nil {
		return nil
	}
	s.cleanOrphansLocked()
	now := time.Now()
	var selected *storeEntry
	for _, entry := range s.entries {
		if entry.desired && entry.path == "" && !entry.fetching && !entry.next.After(now) &&
			(selected == nil || entry.order < selected.order) {
			selected = entry
		}
	}
	if selected == nil {
		return nil
	}
	if s.options.MaxCacheBytes-s.used < s.options.FetchReservationBytes {
		if !errors.Is(selected.err, ErrCapacity) {
			selected.err = errors.Join(ErrCapacity, s.gcErr)
			s.notifyLocked()
		}
		selected.next = now.Add(s.options.RetryInitial)
		selected.order = s.nextOrderLocked()
		return nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, s.options.AttemptTimeout)
	selected.fetching, selected.cancel = true, cancel
	selected.order = s.nextOrderLocked()
	s.used += s.options.FetchReservationBytes
	return &fetchJob{entry: selected, ctx: jobCtx, cancel: cancel}
}

func (s *Store) checkPayload(stage string) (int64, error) {
	children, err := os.ReadDir(stage)
	if err != nil {
		return 0, err
	}
	if len(children) != 1 || children[0].Name() != PayloadName {
		return 0, errors.New("firmware fetch must produce only firmware.bin")
	}
	info, err := os.Lstat(filepath.Join(stage, PayloadName))
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > s.options.MaxPayloadBytes {
		return 0, errors.New("firmware fetch produced an invalid or oversized payload")
	}
	return info.Size(), os.Chmod(filepath.Join(stage, PayloadName), 0444)
}

func (s *Store) finishJob(job *fetchJob, stage string, payloadBytes int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := job.entry
	previousError := entry.err
	entry.fetching, entry.cancel = false, nil
	if job.ctx.Err() != nil {
		err = job.ctx.Err()
	}
	if entry.desired && !s.closed && err == nil {
		final := filepath.Join(s.root, entry.key)
		if _, inspectErr := os.Lstat(final); !os.IsNotExist(inspectErr) {
			err = errors.New("firmware cache publication destination already exists or is unreadable")
		} else {
			err = os.Rename(stage, final)
		}
		if err == nil {
			entry.path, entry.size = filepath.Join(final, PayloadName), payloadBytes
			s.used += entry.size
			entry.err, entry.backoff = nil, 0
		}
	}
	// Cleanup precedes releasing the reservation, while new jobs are excluded by
	// the mutex. Failed cleanup stays charged until a later cleanup succeeds.
	var cleanupErr error
	if stage != "" {
		cleanupErr = s.removeAll(stage)
	}
	if cleanupErr == nil {
		s.used -= s.options.FetchReservationBytes
	} else {
		s.orphans[stage] = s.options.FetchReservationBytes
		s.gcErr = fmt.Errorf("remove firmware image staging: %w", cleanupErr)
		err = errors.Join(err, s.gcErr)
	}
	if !entry.desired || s.closed {
		s.pruneLocked(entry)
		s.notifyLocked()
		return
	}
	if err != nil {
		entry.err = fmt.Errorf("acquire firmware image %s: %w", entry.reference, err)
		if entry.backoff == 0 {
			entry.backoff = s.options.RetryInitial
		} else if entry.backoff >= s.options.RetryMax/2 {
			entry.backoff = s.options.RetryMax
		} else {
			entry.backoff *= 2
		}
		entry.next = time.Now().Add(entry.backoff)
	}
	s.signalWorkersLocked()
	if entry.path != "" || !sameStoreError(previousError, entry.err) {
		s.notifyLocked()
	}
}

func sameStoreError(a, b error) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Error() == b.Error()
}

func (s *Store) pruneLocked(entry *storeEntry) bool {
	if entry.desired || entry.fetching || entry.pins != 0 {
		return false
	}
	if entry.path != "" {
		if err := s.removeAll(filepath.Dir(entry.path)); err != nil {
			s.gcErr = fmt.Errorf("remove obsolete firmware payload: %w", err)
			return false
		}
		s.used -= entry.size
	}
	delete(s.entries, entry.reference)
	return true
}

func (s *Store) cleanOrphansLocked() {
	for directory, reservation := range s.orphans {
		if err := s.removeAll(directory); err != nil {
			s.gcErr = fmt.Errorf("remove firmware image staging: %w", err)
			continue
		}
		s.used -= reservation
		delete(s.orphans, directory)
		s.notifyLocked()
	}
}

func (s *Store) nextOrderLocked() uint64 {
	s.sequence++
	return s.sequence
}

func (s *Store) signalWorkersLocked() {
	for range s.options.Workers {
		select {
		case s.wake <- struct{}{}:
		default:
			return
		}
	}
}

func (s *Store) notifyLocked() {
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

func (s *Store) closeOwnerLocked() {
	if !s.closed || s.workers != 0 || s.owner == nil {
		return
	}
	for _, entry := range s.entries {
		if entry.pins != 0 {
			return
		}
	}
	_ = s.owner.Close()
	s.owner = nil
}
