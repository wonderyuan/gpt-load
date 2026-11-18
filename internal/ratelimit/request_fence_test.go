package ratelimit

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAccessKeyFenceBusyDrainAndIndependentAdmission(t *testing.T) {
	// Literal construction must work without initializing any maps.
	c := &Concurrency{}
	request, ok := c.TryAcquireRequest(10, 2, 1)
	if !ok {
		t.Fatal("initial request rejected")
	}
	t.Cleanup(request)
	group, ok := c.TryAcquireGroup(7, 1)
	if !ok {
		t.Fatal("initial group rejected")
	}
	t.Cleanup(group)
	before := c.Snapshot()
	fence, ok := c.BeginAccessKeyFence([]uint{10, 11})
	if !ok || fence == nil {
		t.Fatal("busy key fence rejected")
	}
	t.Cleanup(fence)
	for _, key := range []uint{10, 11} {
		assertFencedRequest(t, c, key)
	}
	if after := c.Snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("fence changed counters: before=%+v after=%+v", before, after)
	}

	other, ok := c.TryAcquireRequest(20, 2, 1)
	if !ok {
		t.Fatal("fence consumed capacity or blocked an unrelated key")
	}
	t.Cleanup(other)
	if extra, ok := c.TryAcquireRequest(21, 2, 0); ok {
		extra()
		t.Fatal("global limit stopped applying to unrelated keys")
	}
	otherGroup, ok := c.TryAcquireGroup(8, 1)
	if !ok {
		t.Fatal("fence blocked an unrelated group")
	}
	t.Cleanup(otherGroup)
	otherGroup()

	request()
	request()
	if got := c.AccessKeyCurrent(10); got != 0 {
		t.Fatalf("existing request did not drain: %d", got)
	}
	if got := c.Snapshot(); got.Global != 1 || got.AccessKeys[20] != 1 || got.Groups[7] != 1 {
		t.Fatalf("drain changed unrelated counters: %+v", got)
	}
	assertFencedRequest(t, c, 10)
	fence()
	resumed, ok := c.TryAcquireRequest(10, 2, 1)
	if !ok {
		t.Fatal("key did not resume synchronously after fence release")
	}
	resumed()
	other()
	group()
	assertFenceCountersEmpty(t, c)
}

func TestAccessKeyFenceNestedOwnersAndIdempotentRelease(t *testing.T) {
	c := NewConcurrency()
	ids := []uint{10, 11}
	outer, ok := c.BeginAccessKeyFence(ids)
	if !ok {
		t.Fatal("outer fence rejected")
	}
	t.Cleanup(outer)
	// Releasing must use the owned IDs, not the caller's mutable slice.
	ids[0] = 99
	inner, ok := c.BeginAccessKeyFence([]uint{11, 12})
	if !ok {
		t.Fatal("overlapping fence rejected")
	}
	t.Cleanup(inner)
	if !reflect.DeepEqual(c.accessKeyFences, map[uint]int64{10: 1, 11: 2, 12: 1}) {
		t.Fatalf("incorrect owner counts: %v", c.accessKeyFences)
	}

	var done sync.WaitGroup
	for range 32 {
		done.Add(1)
		go func() {
			defer done.Done()
			outer()
		}()
	}
	done.Wait()
	if !reflect.DeepEqual(c.accessKeyFences, map[uint]int64{11: 1, 12: 1}) {
		t.Fatalf("outer release removed another owner's protection: %v", c.accessKeyFences)
	}
	for _, key := range []uint{11, 12} {
		assertFencedRequest(t, c, key)
	}
	request, ok := c.TryAcquireRequest(10, 0, 0)
	if !ok {
		t.Fatal("outer-only key did not resume")
	}
	request()
	inner()
	inner()
	outer()
	assertFenceCountersEmpty(t, c)

	// An old owner's repeated release must not release a newly installed fence.
	next, ok := c.BeginAccessKeyFence([]uint{11})
	if !ok {
		t.Fatal("new owner rejected")
	}
	t.Cleanup(next)
	inner()
	assertFencedRequest(t, c, 11)
	next()
	assertFenceCountersEmpty(t, c)
}

func TestAccessKeyFenceInvalidBatchHasNoPartialEffect(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []uint
	}{
		{"nil", nil},
		{"empty", []uint{}},
		{"zero", []uint{0}},
		{"zero_after_valid", []uint{11, 10, 0}},
		{"duplicate", []uint{11, 10, 11}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Invalid input must not even initialize the zero-value fence map.
			c := &Concurrency{}
			if release, ok := c.BeginAccessKeyFence(tc.ids); ok || release != nil {
				t.Fatal("invalid batch accepted")
			}
			if c.accessKeyFences != nil {
				t.Fatalf("invalid batch mutated fence map: %v", c.accessKeyFences)
			}
			owner, ok := c.BeginAccessKeyFence([]uint{10})
			if !ok {
				t.Fatal("existing owner rejected")
			}
			t.Cleanup(owner)
			before := c.Snapshot()
			if release, ok := c.BeginAccessKeyFence(tc.ids); ok || release != nil {
				t.Fatal("invalid batch accepted with existing owner")
			}
			if !reflect.DeepEqual(c.accessKeyFences, map[uint]int64{10: 1}) {
				t.Fatalf("invalid batch partially changed existing fences: %v", c.accessKeyFences)
			}
			assertFencedRequest(t, c, 10)
			request, ok := c.TryAcquireRequest(11, 0, 0)
			if !ok {
				t.Fatal("invalid batch partially fenced a valid key")
			}
			request()
			if after := c.Snapshot(); after.Global != before.Global || len(after.AccessKeys) != 0 || len(after.Groups) != 0 {
				t.Fatalf("invalid batch changed counters: %+v", after)
			}
			owner()
			assertFenceCountersEmpty(t, c)
		})
	}
}

func TestAccessKeyFenceAtomicRequestRace(t *testing.T) {
	const workers = 64
	for round := range 32 {
		c := &Concurrency{}
		start := make(chan struct{})
		fenced := make(chan struct{})
		finish := make(chan struct{})
		var ready, done sync.WaitGroup
		var admitted, admittedAfterFence atomic.Int64
		for i := range workers {
			ready.Add(1)
			done.Add(1)
			go func(key uint) {
				defer done.Done()
				<-start
				request, ok := c.TryAcquireRequest(key, 0, 0)
				if ok {
					admitted.Add(1)
				}
				<-fenced
				if extra, allowed := c.TryAcquireRequest(key, 0, 0); allowed {
					admittedAfterFence.Add(1)
					extra()
				}
				ready.Done()
				<-finish
				if ok {
					request()
					request()
				}
			}(uint(i%4 + 10))
		}
		fenceResult := make(chan func(), 1)
		go func() {
			<-start
			release, ok := c.BeginAccessKeyFence([]uint{10, 11, 12, 13})
			if !ok {
				fenceResult <- nil
			} else {
				fenceResult <- release
			}
			close(fenced)
		}()
		close(start)
		fence := <-fenceResult
		ready.Wait()
		current := c.Snapshot()
		refs := len(c.accessKeyFences)
		close(finish)
		done.Wait()
		if fence == nil {
			t.Fatalf("round %d: valid fence rejected", round)
		}
		fence()
		if admittedAfterFence.Load() != 0 || refs != 4 {
			t.Fatalf("round %d: partial fence or post-fence admission: refs=%d admitted=%d", round, refs, admittedAfterFence.Load())
		}
		if current.Global != admitted.Load() {
			t.Fatalf("round %d: pre-fence admission not tracked: current=%+v admitted=%d", round, current, admitted.Load())
		}
		for _, key := range []uint{10, 11, 12, 13} {
			if got := c.AccessKeyCurrent(key); got != 0 {
				t.Fatalf("round %d: key %d did not drain: %d", round, key, got)
			}
			request, ok := c.TryAcquireRequest(key, 0, 0)
			if !ok {
				t.Fatalf("round %d: key %d did not resume", round, key)
			}
			request()
		}
		assertFenceCountersEmpty(t, c)
	}
}

func assertFencedRequest(t *testing.T, c *Concurrency, key uint) {
	t.Helper()
	release, ok := c.TryAcquireRequest(key, 0, 0)
	if ok {
		release()
		t.Fatalf("fenced key %d admitted", key)
	}
	if release != nil {
		t.Fatalf("fenced key %d returned a release function", key)
	}
}

func assertFenceCountersEmpty(t *testing.T, c *Concurrency) {
	t.Helper()
	if s := c.Snapshot(); s.Global != 0 || len(s.AccessKeys) != 0 || len(s.Groups) != 0 {
		t.Fatalf("leaked counters: %+v", s)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.accessKeyFences) != 0 {
		t.Fatalf("leaked fences: %v", c.accessKeyFences)
	}
}
