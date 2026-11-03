// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// http://www.apache.org/licenses/LICENSE-2.0

package firmwareimage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFirmwareImages(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Firmware image store")
}

func testRef(digit string) string {
	return "registry.example/firmware@sha256:" + strings.Repeat(digit, 64)
}

func smallStoreOptions() StoreOptions {
	return StoreOptions{
		Workers: 2, MaxImages: 4, MaxCacheBytes: 256, MaxPayloadBytes: 16, FetchReservationBytes: 64,
		AttemptTimeout: time.Second, RetryInitial: 10 * time.Millisecond, RetryMax: 50 * time.Millisecond,
	}
}

func writeTestPayload(_ context.Context, _ string, destination string) error {
	return os.WriteFile(filepath.Join(destination, PayloadName), []byte("firmware"), 0600)
}

func testStore(fetch FetchFunc, options StoreOptions) *Store {
	s, err := NewStore(GinkgoT().TempDir(), fetch, options)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(s.Close)
	return s
}

func runTestStore(s *Store) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	DeferCleanup(func() {
		cancel()
		Eventually(done, 3*time.Second).Should(Receive(Succeed()))
	})
}

func readyArtifact(s *Store, ref string) (string, func()) {
	var path string
	var release func()
	Eventually(func() error {
		var err error
		path, release, err = s.Acquire(ref)
		return err
	}, 3*time.Second, time.Millisecond).Should(Succeed())
	return path, release
}

var _ = Describe("Asynchronous firmware image store", func() {
	It("returns pending immediately and keeps a blocked reference from delaying another", func() {
		started := make(chan struct{})
		var slowAttempts atomic.Int32
		s := testStore(func(ctx context.Context, ref, destination string) error {
			if ref == testRef("a") {
				slowAttempts.Add(1)
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}
			return writeTestPayload(ctx, ref, destination)
		}, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a"), testRef("b")})).To(Succeed())
		runTestStore(s)
		Eventually(started).Should(BeClosed())
		for range 20 {
			_, release, err := s.Acquire(testRef("a"))
			Expect(err).To(MatchError(ErrPending))
			Expect(release).To(BeNil())
		}
		path, release := readyArtifact(s, testRef("b"))
		defer release()
		Expect(os.ReadFile(path)).To(Equal([]byte("firmware")))
		Expect(slowAttempts.Load()).To(Equal(int32(1)))
	})

	It("keeps a removed artifact until the last idempotent release", func() {
		options := smallStoreOptions()
		options.MaxImages = 1
		s := testStore(writeTestPayload, options)
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		path, release := readyArtifact(s, testRef("a"))
		_, releaseSecond, err := s.Acquire(testRef("a"))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.SetDesired([]string{testRef("b")})).To(Succeed())
		Expect(os.ReadFile(path)).To(Equal([]byte("firmware")))
		_, _, err = s.Acquire(testRef("b"))
		Expect(errors.Is(err, ErrCapacity)).To(BeTrue())
		release()
		release()
		Expect(os.ReadFile(path)).To(Equal([]byte("firmware")))
		releaseSecond()
		_, err = os.Stat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())
		Expect(s.SetDesired([]string{testRef("b")})).To(Succeed())
		_, releaseNew := readyArtifact(s, testRef("b"))
		releaseNew()
	})

	It("does not notify reconciliation for an ordinary ready acquire and release", func() {
		s := testStore(writeTestPayload, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		_, release := readyArtifact(s, testRef("a"))
		release()
		for len(s.changes) != 0 {
			<-s.changes
		}
		for range 10 {
			_, release, err := s.Acquire(testRef("a"))
			Expect(err).NotTo(HaveOccurred())
			release()
		}
		Consistently(s.Changes(), 30*time.Millisecond, time.Millisecond).ShouldNot(Receive())
	})

	It("retains a failed-cleanup reservation until the staging directory is actually removed", func() {
		options := smallStoreOptions()
		options.MaxCacheBytes = options.FetchReservationBytes
		var attempts atomic.Int32
		var refuseCleanup atomic.Bool
		refuseCleanup.Store(true)
		s := testStore(func(ctx context.Context, ref, destination string) error {
			attempts.Add(1)
			if err := writeTestPayload(ctx, ref, destination); err != nil {
				return err
			}
			if ref == testRef("a") {
				return errors.New("failed after staging")
			}
			return nil
		}, options)
		s.removeAll = func(path string) error {
			if strings.HasPrefix(filepath.Base(path), ".staging-") && refuseCleanup.Load() {
				return os.ErrPermission
			}
			return os.RemoveAll(path)
		}
		Expect(s.SetDesired([]string{testRef("a"), testRef("b")})).To(Succeed())
		runTestStore(s)
		Eventually(func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.orphans)
		}).Should(Equal(1))
		s.mu.Lock()
		Expect(s.used).To(Equal(options.FetchReservationBytes))
		for path := range s.orphans {
			_, err := os.Stat(path)
			Expect(err).NotTo(HaveOccurred())
		}
		s.mu.Unlock()
		Consistently(attempts.Load, 30*time.Millisecond, time.Millisecond).Should(Equal(int32(1)))
		refuseCleanup.Store(false)
		Expect(s.SetDesired([]string{testRef("b")})).To(Succeed())
		_, release := readyArtifact(s, testRef("b"))
		release()
		s.mu.Lock()
		Expect(s.orphans).To(BeEmpty())
		Expect(s.used).To(Equal(int64(len("firmware"))))
		s.mu.Unlock()
	})

	It("rejects a completed obsolete attempt even when its transport ignores cancellation", func() {
		started := make(chan struct{})
		finish := make(chan struct{})
		var attempts atomic.Int32
		s := testStore(func(_ context.Context, _ string, destination string) error {
			if attempts.Add(1) == 1 {
				close(started)
				<-finish
				return os.WriteFile(filepath.Join(destination, PayloadName), []byte("obsolete"), 0600)
			}
			return os.WriteFile(filepath.Join(destination, PayloadName), []byte("current"), 0600)
		}, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		Eventually(started).Should(BeClosed())
		Expect(s.SetDesired(nil)).To(Succeed())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		close(finish)
		path, release := readyArtifact(s, testRef("a"))
		defer release()
		Expect(os.ReadFile(path)).To(Equal([]byte("current")))
		Expect(attempts.Load()).To(Equal(int32(2)))
	})

	It("recovers a byte-capacity failure when obsolete ready files are removed", func() {
		options := smallStoreOptions()
		options.Workers = 1
		options.MaxCacheBytes = options.FetchReservationBytes
		s := testStore(writeTestPayload, options)
		Expect(s.SetDesired([]string{testRef("a"), testRef("b")})).To(Succeed())
		runTestStore(s)
		_, release := readyArtifact(s, testRef("a"))
		release()
		Eventually(func() bool {
			_, _, err := s.Acquire(testRef("b"))
			return errors.Is(err, ErrCapacity)
		}).Should(BeTrue())
		Expect(s.SetDesired([]string{testRef("b")})).To(Succeed())
		_, release = readyArtifact(s, testRef("b"))
		release()
	})

	It("preserves already admitted healthy references when new inventory exceeds the bound", func() {
		options := smallStoreOptions()
		options.MaxImages = 2
		s := testStore(writeTestPayload, options)
		Expect(s.SetDesired([]string{testRef("c"), testRef("d")})).To(Succeed())
		runTestStore(s)
		_, releaseC := readyArtifact(s, testRef("c"))
		defer releaseC()
		_, releaseD := readyArtifact(s, testRef("d"))
		defer releaseD()
		Expect(s.SetDesired([]string{testRef("a"), testRef("c"), testRef("d")})).To(Succeed())
		_, _, err := s.Acquire(testRef("a"))
		Expect(errors.Is(err, ErrCapacity)).To(BeTrue())
		_, releaseAgain, err := s.Acquire(testRef("d"))
		Expect(err).NotTo(HaveOccurred())
		releaseAgain()
		s.mu.Lock()
		Expect(s.entries).To(HaveLen(2))
		s.mu.Unlock()
	})

	It("tries other queued images before retrying a failed reference", func() {
		options := smallStoreOptions()
		options.Workers = 1
		attempts := make(chan string, 8)
		s := testStore(func(ctx context.Context, ref, destination string) error {
			attempts <- ref
			if ref == testRef("a") {
				return errors.New("registry unavailable")
			}
			return writeTestPayload(ctx, ref, destination)
		}, options)
		Expect(s.SetDesired([]string{testRef("a"), testRef("b")})).To(Succeed())
		runTestStore(s)
		Eventually(attempts).Should(Receive(Equal(testRef("a"))))
		Eventually(attempts).Should(Receive(Equal(testRef("b"))))
		_, release := readyArtifact(s, testRef("b"))
		release()
	})

	It("retries a failed reference independently and publishes a recovery notification", func() {
		var attempts atomic.Int32
		s := testStore(func(ctx context.Context, ref, destination string) error {
			if attempts.Add(1) == 1 {
				return errors.New("temporary registry failure")
			}
			return writeTestPayload(ctx, ref, destination)
		}, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		Eventually(s.Changes()).Should(Receive())
		_, release := readyArtifact(s, testRef("a"))
		release()
		Expect(attempts.Load()).To(Equal(int32(2)))
	})

	It("does not notify repeated identical acquisition failures", func() {
		var attempts atomic.Int32
		s := testStore(func(_ context.Context, _ string, _ string) error {
			attempts.Add(1)
			return errors.New("unchanged registry failure")
		}, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		Eventually(s.Changes()).Should(Receive())
		Eventually(attempts.Load).Should(BeNumerically(">=", 3))
		Consistently(s.Changes(), 30*time.Millisecond, time.Millisecond).ShouldNot(Receive())
	})

	DescribeTable("rejects invalid transport output without publishing it", func(produce FetchFunc) {
		s := testStore(produce, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		Eventually(func() error {
			_, _, err := s.Acquire(testRef("a"))
			if errors.Is(err, ErrPending) {
				return nil
			}
			return err
		}).Should(HaveOccurred())
		s.mu.Lock()
		Expect(s.entries[testRef("a")].path).To(BeEmpty())
		s.mu.Unlock()
	},
		Entry("an empty file", func(_ context.Context, _ string, dir string) error {
			return os.WriteFile(filepath.Join(dir, PayloadName), nil, 0600)
		}),
		Entry("an oversized file", func(_ context.Context, _ string, dir string) error {
			return os.WriteFile(filepath.Join(dir, PayloadName), make([]byte, 17), 0600)
		}),
		Entry("a symlink", func(_ context.Context, _ string, dir string) error {
			return os.Symlink("/etc/passwd", filepath.Join(dir, PayloadName))
		}),
		Entry("extra files", func(_ context.Context, _ string, dir string) error {
			if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("x"), 0600); err != nil {
				return err
			}
			return writeTestPayload(context.Background(), "", dir)
		}),
	)

	It("limits attempts by context and continues to another image", func() {
		options := smallStoreOptions()
		options.Workers = 1
		options.AttemptTimeout = 20 * time.Millisecond
		s := testStore(func(ctx context.Context, ref, destination string) error {
			if ref == testRef("a") {
				<-ctx.Done()
				return ctx.Err()
			}
			return writeTestPayload(ctx, ref, destination)
		}, options)
		Expect(s.SetDesired([]string{testRef("a"), testRef("b")})).To(Succeed())
		runTestStore(s)
		_, release := readyArtifact(s, testRef("b"))
		release()
		_, _, err := s.Acquire(testRef("a"))
		Expect(errors.Is(err, context.DeadlineExceeded)).To(BeTrue())
	})

	It("rejects malformed inventory without changing existing desired references", func() {
		s := testStore(writeTestPayload, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		Expect(s.SetDesired([]string{"registry.example/firmware:mutable"})).NotTo(Succeed())
		runTestStore(s)
		_, release := readyArtifact(s, testRef("a"))
		release()
	})

	It("supports concurrent acquire, release and desired-inventory updates", func() {
		s := testStore(writeTestPayload, smallStoreOptions())
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		runTestStore(s)
		_, release := readyArtifact(s, testRef("a"))
		release()
		var workers sync.WaitGroup
		for range 8 {
			workers.Go(func() {
				defer GinkgoRecover()
				for range 50 {
					path, release, err := s.Acquire(testRef("a"))
					if err == nil {
						Expect(os.ReadFile(path)).To(Equal([]byte("firmware")))
						release()
					}
				}
			})
		}
		for range 20 {
			Expect(s.SetDesired(nil)).To(Succeed())
			Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		}
		workers.Wait()
	})
})

var _ = Describe("Firmware image cache ownership", func() {
	It("rejects a missing Run context without starting workers", func() {
		s := testStore(writeTestPayload, smallStoreOptions())
		var missingContext context.Context
		Expect(s.Run(missingContext)).To(MatchError("firmware image store context is required"))
		Expect(s.started).To(BeFalse())
	})

	It("exclusively owns its fixed child and preserves unrelated root contents", func() {
		root := GinkgoT().TempDir()
		unrelated := filepath.Join(root, "keep")
		Expect(os.WriteFile(unrelated, []byte("unrelated"), 0600)).To(Succeed())
		first, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Close)
		_, err = NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).To(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(first.root, "stale"), []byte("old"), 0600)).To(Succeed())
		Expect(first.Close()).To(Succeed())
		second, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(second.Close)
		Expect(os.ReadFile(unrelated)).To(Equal([]byte("unrelated")))
		_, err = os.Stat(filepath.Join(second.root, "stale"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("retains its ownership lock and leased path after shutdown until release", func() {
		root := GinkgoT().TempDir()
		s, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		Expect(s.SetDesired([]string{testRef("a")})).To(Succeed())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		path, release := readyArtifact(s, testRef("a"))
		cancel()
		Eventually(done).Should(Receive(Succeed()))
		Expect(os.ReadFile(path)).To(Equal([]byte("firmware")))
		_, err = NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).To(HaveOccurred())
		release()
		next, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).NotTo(HaveOccurred())
		Expect(next.Close()).To(Succeed())
		_, err = os.Stat(path)
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, _, err = s.Acquire(testRef("a"))
		Expect(err).To(MatchError(ErrClosed))
	})

	It("refuses an unowned nonempty store directory", func() {
		root := GinkgoT().TempDir()
		child := filepath.Join(root, "store-v1")
		Expect(os.Mkdir(child, 0700)).To(Succeed())
		unknown := filepath.Join(child, "keep")
		Expect(os.WriteFile(unknown, []byte("unowned"), 0600)).To(Succeed())
		_, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).To(HaveOccurred())
		Expect(os.ReadFile(unknown)).To(Equal([]byte("unowned")))
	})

	It("does not follow an owned-directory symlink during startup cleanup", func() {
		root, other := GinkgoT().TempDir(), GinkgoT().TempDir()
		keep := filepath.Join(other, "keep")
		Expect(os.WriteFile(keep, []byte("keep"), 0600)).To(Succeed())
		Expect(os.Symlink(other, filepath.Join(root, "store-v1"))).To(Succeed())
		_, err := NewStore(root, writeTestPayload, smallStoreOptions())
		Expect(err).To(HaveOccurred())
		Expect(os.ReadFile(keep)).To(Equal([]byte("keep")))
	})
})
