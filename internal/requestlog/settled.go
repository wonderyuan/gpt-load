package requestlog

import (
	"context"
	"errors"
	"time"
)

// AwaitSettled proves that all request logs enqueued at entry have persisted in
// this worker's lifetime without any dropped events. Callers must fence and drain
// the relevant requests before calling; future enqueues do not extend the target.
// fromMS must not predate Start: counters cannot prove losslessness across process
// restarts. This does not flush or prove persistence of quota checkpoints.
// The caller controls the wait bound through ctx; no worker or flush is started.
func (service *Service) AwaitSettled(ctx context.Context, fromMS int64) error {
	if ctx == nil {
		return errors.New("await request log settlement: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service == nil {
		return errors.New("await request log settlement: service is nil")
	}

	service.stateMu.Lock()
	if service.state != lifecycleRunning || service.workerDone == nil {
		service.stateMu.Unlock()
		return errors.New("await request log settlement: worker is not running")
	}
	if service.workerStartedAtMS < 0 || fromMS < service.workerStartedAtMS {
		service.stateMu.Unlock()
		return errors.New("await request log settlement: window predates worker start")
	}
	workerDone := service.workerDone
	targetN := service.enqueuedTotal.Load()
	service.stateMu.Unlock()

	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		service.stateMu.Lock()
		if service.state != lifecycleRunning || service.workerDone != workerDone {
			service.stateMu.Unlock()
			return errors.New("await request log settlement: worker is not running")
		}
		// Read persisted first: a later successful batch must never substitute for
		// an earlier dropped batch. The single FIFO worker records drops before
		// advancing persistedTotal for any subsequent rows.
		persisted := service.persistedTotal.Load()
		lost := service.droppedQueueFullTotal.Load() != 0 ||
			service.droppedPersistFailedTotal.Load() != 0 ||
			service.droppedShutdownTotal.Load() != 0 ||
			service.droppedStoppingTotal.Load() != 0 ||
			service.droppedNotRunningTotal.Load() != 0
		select {
		case <-workerDone:
			service.stateMu.Unlock()
			return errors.New("await request log settlement: worker has exited")
		case <-service.stopRequested:
			service.stateMu.Unlock()
			return errors.New("await request log settlement: worker is stopping")
		default:
		}
		service.stateMu.Unlock()
		if lost {
			return errors.New("await request log settlement: request logs were dropped")
		}
		if persisted >= targetN {
			return ctx.Err()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-workerDone:
			return errors.New("await request log settlement: worker has exited")
		case <-service.stopRequested:
			return errors.New("await request log settlement: worker is stopping")
		case <-timer.C:
			timer.Reset(10 * time.Millisecond)
		}
	}
}
