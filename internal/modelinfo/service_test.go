package modelinfo

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceCoalescesFailuresAndRetainsOwnedHistory(t *testing.T) {
	var homeCalls, speedCalls atomic.Int64
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	service := NewService(NewClient(nil, nil))
	service.now = func() time.Time { return now }
	service.fetchHome = func(context.Context) ([]byte, error) {
		homeCalls.Add(1)
		return nil, errors.New("private detail")
	}
	service.fetchSpeed = func(context.Context) ([]byte, error) {
		speedCalls.Add(1)
		return nil, errors.New("upstream unavailable")
	}
	stamp, speedValue := now.UnixMilli(), 42.0
	service.intelligence = Section[Intelligence]{Status: StatusReady, FetchedAtMS: &stamp, Data: &Intelligence{MaxScore: 150, Models: []ScoreModel{{ID: "model", Name: "Model"}}}}
	service.speed = Section[Speed]{Status: StatusReady, FetchedAtMS: &stamp, UpdatedAtMS: &stamp, Data: &Speed{Models: []SpeedModel{{ID: "model", TokensPerSecond: &speedValue}}}}
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			report, err := service.Read(t.Context())
			if err != nil || report.Intelligence.Status != StatusStale || report.Speed.Status != StatusStale || report.Quota.Status != StatusUnavailable {
				t.Errorf("cached report = %#v, %v", report, err)
				return
			}
			report.Intelligence.Data.Models[0].Name = "caller mutation"
			*report.Speed.Data.Models[0].TokensPerSecond = 1000
		})
	}
	group.Wait()
	if homeCalls.Load() != 1 || speedCalls.Load() != 1 || service.intelligence.Data.Models[0].Name != "Model" || *service.speed.Data.Models[0].TokensPerSecond != 42 {
		t.Fatalf("cache not coalesced or owned: home=%d speed=%d", homeCalls.Load(), speedCalls.Load())
	}
	now = now.Add(retryTTL + time.Second)
	if _, err := service.Read(t.Context()); err != nil || homeCalls.Load() != 2 || speedCalls.Load() != 2 {
		t.Fatalf("retry failed: home=%d speed=%d err=%v", homeCalls.Load(), speedCalls.Load(), err)
	}
	now = now.Add(11 * time.Minute)
	report, err := service.Read(t.Context())
	if err != nil || report.Speed.Status != StatusUnavailable || report.Speed.Data != nil || report.Intelligence.Data == nil {
		t.Fatalf("old speed retained as live: %#v, %v", report, err)
	}
	now = now.Add(25 * time.Hour)
	report, err = service.Read(t.Context())
	if err != nil || report.Intelligence.Data != nil || report.Intelligence.Status != StatusUnavailable {
		t.Fatalf("expired history retained: %#v, %v", report, err)
	}
}

func TestServiceCancellationDoesNotPoisonCache(t *testing.T) {
	service := NewService(NewClient(nil, nil))
	ctx, cancel := context.WithCancel(t.Context())
	service.fetchHome = func(ctx context.Context) ([]byte, error) { cancel(); return nil, ctx.Err() }
	service.fetchSpeed = func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	_, err := service.Read(ctx)
	if !errors.Is(err, context.Canceled) || !service.homeGate.expires.IsZero() || !service.speedGate.expires.IsZero() {
		t.Fatalf("cancellation cached: %v", err)
	}
	service.mu.Lock()
	flight := make(chan struct{})
	service.homeGate.flight = flight
	service.mu.Unlock()
	if service.acquire(ctx, &service.homeGate) {
		t.Fatal("canceled waiter acquired refresh")
	}
	close(flight)
}

func TestServiceSuccessfulCacheAndIndependentRecovery(t *testing.T) {
	home, err := os.ReadFile("testdata/home.html")
	if err != nil {
		t.Fatal(err)
	}
	speed, err := os.ReadFile("testdata/speed.json")
	if err != nil {
		t.Fatal(err)
	}
	_, updated, err := parseSpeed(speed)
	if err != nil || updated == nil {
		t.Fatalf("fixture timestamp: %v", err)
	}
	now := time.UnixMilli(*updated)
	service := NewService(NewClient(nil, nil))
	service.now = func() time.Time { return now }
	var homeCalls, speedCalls int
	failHome := false
	service.fetchHome = func(context.Context) ([]byte, error) {
		homeCalls++
		if failHome {
			return nil, errors.New("unavailable")
		}
		return home, nil
	}
	service.fetchSpeed = func(context.Context) ([]byte, error) { speedCalls++; return speed, nil }
	report, err := service.Read(t.Context())
	if err != nil || report.Intelligence.Status != StatusReady || report.Quota.Status != StatusReady || report.Speed.Status != StatusReady {
		t.Fatalf("first report: %#v, %v", report, err)
	}
	_, _ = service.Read(t.Context())
	if homeCalls != 1 || speedCalls != 1 {
		t.Fatalf("cache missed: %d %d", homeCalls, speedCalls)
	}
	now = now.Add(31 * time.Second)
	_, _ = service.Read(t.Context())
	if homeCalls != 1 || speedCalls != 2 {
		t.Fatalf("independent TTLs: %d %d", homeCalls, speedCalls)
	}
	now = now.Add(homeTTL)
	failHome = true
	report, _ = service.Read(t.Context())
	if report.Intelligence.Status != StatusStale || report.Quota.Status != StatusStale {
		t.Fatalf("last success not retained: %#v", report)
	}
	failHome = false
	now = now.Add(retryTTL + time.Second)
	report, _ = service.Read(t.Context())
	if report.Intelligence.Status != StatusReady || report.Quota.Status != StatusReady {
		t.Fatalf("recovery failed: %#v", report)
	}
}

func TestHomeSectionsFailIndependently(t *testing.T) {
	home, err := os.ReadFile("testdata/home.html")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewClient(nil, nil))
	service.fetchHome = func(context.Context) ([]byte, error) {
		partial := strings.ReplaceAll(string(home), `data-intelligence-ui`, `data-unrelated-ui`)
		partial = strings.ReplaceAll(partial, `data-cir-panel="total"`, `data-cir-panel="missing"`)
		partial = strings.ReplaceAll(partial, `data-mtr-payload`, `data-unrelated-payload`)
		return []byte(partial), nil
	}
	service.fetchSpeed = func(context.Context) ([]byte, error) { return nil, errors.New("offline") }
	report, err := service.Read(t.Context())
	if err != nil || report.Intelligence.Status != StatusUnavailable || report.Quota.Status != StatusReady || report.Quota.Data == nil {
		t.Fatalf("partial failure blanked valid quota: %#v, %v", report, err)
	}
}

func TestSpeedSourceAgeIsNotResetBySuccessfulFetch(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		age  time.Duration
		want Status
	}{
		{time.Minute, StatusReady},
		{3 * time.Minute, StatusStale},
		{11 * time.Minute, StatusUnavailable},
		{-2 * time.Minute, StatusUnavailable},
	} {
		updated, fetched := now.Add(-test.age).UnixMilli(), now.UnixMilli()
		section := ageSpeed(Section[Speed]{Status: StatusReady, UpdatedAtMS: &updated, FetchedAtMS: &fetched, Data: &Speed{}}, now)
		if section.Status != test.want {
			t.Fatalf("age %s = %s, want %s", test.age, section.Status, test.want)
		}
	}
}
