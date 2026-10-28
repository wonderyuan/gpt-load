package modelinfo

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

const (
	homeTTL  = 10 * time.Minute
	speedTTL = 30 * time.Second
	retryTTL = 30 * time.Second
)

type refreshGate struct {
	expires time.Time
	flight  chan struct{}
}

type Service struct {
	mu           sync.Mutex
	homeGate     refreshGate
	speedGate    refreshGate
	intelligence Section[Intelligence]
	quota        Section[Quota]
	speed        Section[Speed]
	fetchHome    func(context.Context) ([]byte, error)
	fetchSpeed   func(context.Context) ([]byte, error)
	now          func() time.Time
}

func NewService(client *Client) *Service {
	return &Service{
		fetchHome: client.fetchHome, fetchSpeed: client.fetchSpeed, now: time.Now,
		intelligence: Section[Intelligence]{Status: StatusUnavailable},
		quota:        Section[Quota]{Status: StatusUnavailable},
		speed:        Section[Speed]{Status: StatusUnavailable},
	}
}

// Read refreshes only expired public datasets; callers cannot bypass the shared cache.
func (service *Service) Read(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	var group sync.WaitGroup
	group.Go(func() { service.refreshHome(ctx) })
	group.Go(func() { service.refreshSpeed(ctx) })
	group.Wait()
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	now := service.now()
	report := Report{
		ObservedAtMS: now.UnixMilli(),
		Intelligence: ageSection(service.intelligence, now, 24*time.Hour),
		Speed:        ageSpeed(service.speed, now),
		Quota:        ageSection(service.quota, now, 24*time.Hour),
	}
	// Returned slices and numeric pointers must not expose mutable shared cache state.
	encoded, err := json.Marshal(report)
	if err != nil {
		return Report{}, err
	}
	var owned Report
	err = json.Unmarshal(encoded, &owned)
	return owned, err
}

func (service *Service) acquire(ctx context.Context, gate *refreshGate) bool {
	for {
		service.mu.Lock()
		if ctx.Err() != nil {
			service.mu.Unlock()
			return false
		}
		if gate.flight != nil {
			flight := gate.flight
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return false
			case <-flight:
				continue
			}
		}
		if service.now().Before(gate.expires) {
			service.mu.Unlock()
			return false
		}
		gate.flight = make(chan struct{})
		service.mu.Unlock()
		return true
	}
}

func (service *Service) finish(gate *refreshGate, ctx context.Context, ttl time.Duration) {
	if ctx.Err() == nil {
		gate.expires = service.now().Add(ttl)
	}
	close(gate.flight)
	gate.flight = nil
}

func (service *Service) refreshHome(ctx context.Context) {
	if !service.acquire(ctx, &service.homeGate) {
		return
	}
	payload, fetchErr := service.fetchHome(ctx)
	var intelligence Intelligence
	var quota Quota
	var intelligenceAt, quotaAt *int64
	intelligenceErr, quotaErr := fetchErr, fetchErr
	if fetchErr == nil {
		intelligence, intelligenceAt, intelligenceErr = parseIntelligence(payload)
		quota, quotaAt, quotaErr = parseQuota(payload)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	ttl := homeTTL
	if intelligenceErr != nil || quotaErr != nil {
		ttl = retryTTL
	}
	if ctx.Err() == nil {
		service.intelligence = updateSection(service.intelligence, intelligence, intelligenceAt, intelligenceErr, service.now())
		service.quota = updateSection(service.quota, quota, quotaAt, quotaErr, service.now())
	}
	service.finish(&service.homeGate, ctx, ttl)
}

func (service *Service) refreshSpeed(ctx context.Context) {
	if !service.acquire(ctx, &service.speedGate) {
		return
	}
	payload, err := service.fetchSpeed(ctx)
	var data Speed
	var updated *int64
	if err == nil {
		data, updated, err = parseSpeed(payload)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if ctx.Err() == nil {
		service.speed = updateSection(service.speed, data, updated, err, service.now())
	}
	service.finish(&service.speedGate, ctx, speedTTL)
}

func updateSection[T any](previous Section[T], data T, updated *int64, err error, now time.Time) Section[T] {
	if err != nil {
		if previous.Data != nil {
			previous.Status = StatusStale
			return previous
		}
		return Section[T]{Status: StatusUnavailable}
	}
	fetched := now.UnixMilli()
	return Section[T]{Status: StatusReady, FetchedAtMS: &fetched, UpdatedAtMS: updated, Data: &data}
}

func ageSpeed(section Section[Speed], now time.Time) Section[Speed] {
	section = ageSection(section, now, 10*time.Minute)
	if section.Data == nil {
		return section
	}
	if section.UpdatedAtMS == nil || time.UnixMilli(*section.UpdatedAtMS).After(now.Add(time.Minute)) || now.Sub(time.UnixMilli(*section.UpdatedAtMS)) > 10*time.Minute {
		return Section[Speed]{Status: StatusUnavailable, FetchedAtMS: section.FetchedAtMS, UpdatedAtMS: section.UpdatedAtMS}
	}
	if now.Sub(time.UnixMilli(*section.UpdatedAtMS)) > 2*time.Minute {
		section.Status = StatusStale
	}
	return section
}

func ageSection[T any](section Section[T], now time.Time, maxAge time.Duration) Section[T] {
	if section.FetchedAtMS != nil && now.Sub(time.UnixMilli(*section.FetchedAtMS)) > maxAge {
		return Section[T]{Status: StatusUnavailable, FetchedAtMS: section.FetchedAtMS, UpdatedAtMS: section.UpdatedAtMS}
	}
	return section
}
