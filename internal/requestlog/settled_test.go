package requestlog

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/storage/models"
)

const settledTestStartMS int64 = 1000

func newSettledTestService(t *testing.T, writer batchWriter) (*Service, *manualTimerFactory) {
	t.Helper()
	timers := newManualTimerFactory()
	service := newService(writer, nil, timers.New)
	service.now = func() time.Time { return time.UnixMilli(settledTestStartMS) }
	service.logger = logrus.New()
	service.logger.SetOutput(io.Discard)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
	})
	return service, timers
}

// The second Err call observes the first predicate check, after the entry target
// has been captured. This coordinates tests without adding a production hook.
type settledObservedContext struct {
	context.Context
	checks  atomic.Int32
	entered chan struct{}
}

func (ctx *settledObservedContext) Err() error {
	if ctx.checks.Add(1) == 2 {
		close(ctx.entered)
	}
	return ctx.Context.Err()
}

func beginSettledWait(t *testing.T, service *Service, ctx context.Context) <-chan error {
	t.Helper()
	observed := &settledObservedContext{Context: ctx, entered: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- service.AwaitSettled(observed, settledTestStartMS) }()
	receiveValue(t, observed.entered)
	return result
}

func assertSettledPending(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("AwaitSettled returned before persistence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestAwaitSettledWaitsForPriorFIFOBatchesNotFutureEnqueues(t *testing.T) {
	type pendingWrite struct {
		ids     []string
		release chan struct{}
	}
	writes := make(chan pendingWrite, 3)
	service, timers := newSettledTestService(t, batchWriterFunc(func(ctx context.Context, rows []models.RequestLog) error {
		if len(rows) == 1 && rows[0].ID == "future-other-key" {
			return nil
		}
		write := pendingWrite{release: make(chan struct{})}
		for _, row := range rows {
			write.ids = append(write.ids, row.ID)
		}
		writes <- write
		select {
		case <-write.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.Emit(testEvent("prior-first"))
	receiveValue(t, timers.created).Fire()
	first := receiveValue(t, writes)
	defer close(first.release)
	service.Emit(testEvent("prior-second"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := beginSettledWait(t, service, ctx)
	assertSettledPending(t, result)

	first.release <- struct{}{}
	secondTimer := receiveValue(t, timers.created)
	assertSettledPending(t, result) // The earlier batch alone cannot satisfy N=2.
	secondTimer.Fire()
	second := receiveValue(t, writes)
	defer close(second.release)
	service.Emit(testEvent("future-other-key"))
	assertSettledPending(t, result)
	second.release <- struct{}{}
	// The next batch has left the queue, but its flush timer has not fired.
	receiveValue(t, timers.created)
	if err := receiveValue(t, result); err != nil {
		t.Fatalf("AwaitSettled() error = %v", err)
	}
	if strings.Join(first.ids, ",") != "prior-first" || strings.Join(second.ids, ",") != "prior-second" {
		t.Fatalf("FIFO writes = %v, %v", first.ids, second.ids)
	}
	if stats := service.Stats(); stats.EnqueuedTotal != 3 || stats.PersistedTotal != 2 {
		t.Fatalf("entry target must not follow future logs: %+v", stats)
	}
}

func TestAwaitSettledRejectsEveryDropCounter(t *testing.T) {
	for name, counter := range map[string]func(*Service) *atomic.Uint64{
		"queue_full":     func(s *Service) *atomic.Uint64 { return &s.droppedQueueFullTotal },
		"persist_failed": func(s *Service) *atomic.Uint64 { return &s.droppedPersistFailedTotal },
		"shutdown":       func(s *Service) *atomic.Uint64 { return &s.droppedShutdownTotal },
		"stopping":       func(s *Service) *atomic.Uint64 { return &s.droppedStoppingTotal },
		"not_running":    func(s *Service) *atomic.Uint64 { return &s.droppedNotRunningTotal },
	} {
		t.Run(name, func(t *testing.T) {
			service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
			if err := service.Start(); err != nil {
				t.Fatal(err)
			}
			counter(service).Add(1)
			if err := service.AwaitSettled(context.Background(), settledTestStartMS); err == nil {
				t.Fatal("AwaitSettled accepted a dropped event with an empty target")
			}
		})
	}
}

func TestAwaitSettledRejectsDropDuringWaitEvenWhenTargetIsPersisted(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.enqueuedTotal.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := beginSettledWait(t, service, ctx)
	service.droppedPersistFailedTotal.Add(1)
	service.persistedTotal.Store(1) // A future success cannot substitute for loss.
	if err := receiveValue(t, result); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitSettled() error = %v, want immediate loss refusal", err)
	}
}

func TestAwaitSettledRejectsWriteAndProjectionFailures(t *testing.T) {
	for _, projection := range []bool{false, true} {
		name := "write"
		if projection {
			name = "projection"
		}
		t.Run(name, func(t *testing.T) {
			service, timers := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error {
				return errors.New("injected secret must not appear in barrier error")
			}))
			if err := service.Start(); err != nil {
				t.Fatal(err)
			}
			event := testEvent("failed")
			if projection {
				event.CompletedAt = time.Unix(-1, 0)
			}
			service.Emit(event)
			timer := receiveValue(t, timers.created)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result := beginSettledWait(t, service, ctx)
			timer.Fire()
			err := receiveValue(t, result)
			if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "dropped") {
				t.Fatalf("AwaitSettled() error = %v, want loss refusal", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("barrier error exposes writer contents: %v", err)
			}
		})
	}
}

func TestAwaitSettledContextBounds(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.AwaitSettled(canceled, settledTestStartMS); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-canceled empty target error = %v", err)
	}
	service.Emit(testEvent("pending"))
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := service.AwaitSettled(ctx, settledTestStartMS); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	ctx, cancelWait := context.WithCancel(context.Background())
	result := beginSettledWait(t, service, ctx)
	cancelWait()
	if err := receiveValue(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during wait error = %v", err)
	}
	if err := service.AwaitSettled(nil, settledTestStartMS); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestAwaitSettledRequiresRunningWorker(t *testing.T) {
	var nilService *Service
	if err := nilService.AwaitSettled(context.Background(), settledTestStartMS); err == nil {
		t.Fatal("nil service accepted")
	}
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	if err := service.AwaitSettled(context.Background(), settledTestStartMS); err == nil {
		t.Fatal("unstarted service accepted")
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.AwaitSettled(context.Background(), settledTestStartMS); err == nil {
		t.Fatal("stopped service accepted")
	}
}

func TestAwaitSettledRejectsStopDuringWait(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.Emit(testEvent("pending"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := beginSettledWait(t, service, ctx)
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := receiveValue(t, result); err == nil {
		t.Fatal("worker stopped during wait but barrier succeeded")
	}
}

func TestAwaitSettledRejectsExitedWorker(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.workerCancel()
	receiveValue(t, service.workerDone)
	if err := service.AwaitSettled(context.Background(), settledTestStartMS); err == nil {
		t.Fatal("exited worker accepted while lifecycle still says running")
	}
}

func TestAwaitSettledWindowMustStartInWorkerLifetime(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	constructedAt := time.UnixMilli(10)
	now := constructedAt
	service.now = func() time.Time { return now }
	now = time.UnixMilli(settledTestStartMS)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if service.workerStartedAtMS != now.UnixMilli() {
		t.Fatalf("worker start = %d, want launch clock %d", service.workerStartedAtMS, now.UnixMilli())
	}
	for _, fromMS := range []int64{-1, constructedAt.UnixMilli(), settledTestStartMS - 1} {
		if err := service.AwaitSettled(context.Background(), fromMS); err == nil {
			t.Fatalf("pre-start window %d accepted", fromMS)
		}
	}
	for _, fromMS := range []int64{settledTestStartMS, settledTestStartMS + 1} {
		if err := service.AwaitSettled(context.Background(), fromMS); err != nil {
			t.Fatalf("current lifetime window %d rejected: %v", fromMS, err)
		}
	}
}

func TestAwaitSettledDoesNotFlushOrAwaitQuotaCheckpoints(t *testing.T) {
	var writerCalls atomic.Int32
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error {
		writerCalls.Add(1)
		return nil
	}))
	flusher := &fakePassiveQuotaFlusher{}
	service.SetPassiveQuotaFlusher(flusher)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	service.accessQuotaCheckpointDegraded.Store(true)
	service.accessQuotaCheckpointWriteFailureTotal.Store(1)
	if err := service.AwaitSettled(context.Background(), settledTestStartMS); err != nil {
		t.Fatalf("independent quota checkpoint state affected request log proof: %v", err)
	}
	if writerCalls.Load() != 0 || flusher.callCount() != 0 || len(service.quotaWake) != 0 {
		t.Fatal("AwaitSettled triggered a writer or quota flush")
	}
}

func TestAwaitSettledWorkerStartIsNonnegative(t *testing.T) {
	service, _ := newSettledTestService(t, batchWriterFunc(func(context.Context, []models.RequestLog) error { return nil }))
	service.now = func() time.Time { return time.UnixMilli(-1) }
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if service.workerStartedAtMS != 0 {
		t.Fatalf("negative launch clock produced start %d, want 0", service.workerStartedAtMS)
	}
	if err := service.AwaitSettled(context.Background(), -1); err == nil {
		t.Fatal("negative window accepted")
	}
}
