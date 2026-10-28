package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/requestlog"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type forkCycleUsageReader struct {
	queries []requestlog.CredentialWindowUsageQuery
	result  requestlog.CredentialWindowUsage
	err     error
}

func (r *forkCycleUsageReader) QueryCredentialWindowUsage(_ context.Context, query requestlog.CredentialWindowUsageQuery) (requestlog.CredentialWindowUsage, error) {
	r.queries = append(r.queries, query)
	result := r.result
	result.Source = query.Source
	return result, r.err
}

func forkCycleUsage(cost int64) requestlog.CredentialWindowUsage {
	return requestlog.CredentialWindowUsage{
		DataComplete:   true,
		UsageAggregate: requestlog.UsageAggregate{RequestCount: 1, SuccessCount: 1, EstimatedCostNanoUSD: cost},
	}
}

func forkCycleWindow(id string, seconds, reset int64, ratio float64) ObservationQuotaWindow {
	return ObservationQuotaWindow{ID: id, Scope: "account", Unit: "percent", State: "available",
		WindowSeconds: &seconds, ResetAtMS: &reset, Utilization: &ratio}
}

func forkCycleObservation(at int64, windows ...ObservationQuotaWindow) *CredentialObservationResponse {
	return &CredentialObservationResponse{State: "fresh", ObservedAtMS: &at,
		Snapshot: &CredentialObservationSnapshot{Plan: ObservationPlanSummary{Name: "pro"}, QuotaWindows: windows}}
}

func TestForkAccountCycleGLMProFixedCredits(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	group := models.Group{ChannelID: string(channel.ZhipuAI), ConnectionType: models.ConnectionTypeAPIKey,
		Params:    models.JSON(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`),
		Overrides: models.JSON(`{"glm_coding_credit_enabled":true}`)}
	reader := &forkCycleUsageReader{err: errors.New("fixed credits must not query local cost")}
	fixture.service.credentialWindowUsage = reader
	for _, plan := range []string{"pro", "glm-pro"} {
		for _, ratio := range []float64{0, 0.64} {
			t.Run(fmt.Sprintf("%s/%g", plan, ratio), func(t *testing.T) {
				observation := forkCycleObservation(now-60_000,
					forkCycleWindow("5h", 18000, now+3_600_000, ratio),
					forkCycleWindow("weekly", 604800, now+86_400_000, ratio))
				observation.Snapshot.Plan.Name = plan
				before, _ := json.Marshal(observation)
				got := fixture.service.forkAccountCycleEstimates(t.Context(), group, 7, observation, now)
				if len(got) != 2 {
					t.Fatalf("estimates=%#v", got)
				}
				wantUsed, wantRemaining := []string{"0", "0"}, []string{"12000", "60000"}
				if ratio == 0.64 {
					wantUsed, wantRemaining = []string{"7680", "38400"}, []string{"4320", "21600"}
				}
				for i, total := range []string{"12000", "60000"} {
					if got[i].Total != total || got[i].Used != wantUsed[i] || got[i].Remaining != wantRemaining[i] ||
						got[i].Unit != "glm_coding_credits" || got[i].Method != "fixed_plan" || !got[i].Estimated || !got[i].Complete {
						t.Fatalf("estimate=%#v", got[i])
					}
				}
				after, _ := json.Marshal(observation)
				if string(before) != string(after) || len(reader.queries) != 0 {
					t.Fatal("fixed estimate mutated shared windows or queried local usage")
				}
			})
		}
	}
	for _, test := range []struct{ name, plan, target, overrides string }{
		{"unknown plan", "unknown", "https://open.bigmodel.cn/api/coding/paas/v4", `{"glm_coding_credit_enabled":true}`},
		{"lite", "lite", "https://open.bigmodel.cn/api/coding/paas/v4", `{"glm_coding_credit_enabled":true}`},
		{"not opted in", "pro", "https://open.bigmodel.cn/api/coding/paas/v4", `{}`},
		{"PAYG", "pro", "https://open.bigmodel.cn/api/paas/v4", `{"glm_coding_credit_enabled":true}`},
		{"proxy", "pro", "https://proxy.example/api/coding/paas/v4", `{"glm_coding_credit_enabled":true}`},
		{"international", "pro", "https://api.z.ai/api/coding/paas/v4", `{"glm_coding_credit_enabled":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := group
			copy.Params = models.JSON(fmt.Sprintf(`{"base_url":%q}`, test.target))
			copy.Overrides = models.JSON(test.overrides)
			observation := forkCycleObservation(now-60_000, forkCycleWindow("5h", 18000, now+3_600_000, 0.64))
			observation.Snapshot.Plan.Name = test.plan
			if got := fixture.service.forkAccountCycleEstimates(t.Context(), copy, 7, observation, now); got == nil || len(got) != 0 {
				t.Fatalf("ineligible GLM estimate=%#v", got)
			}
		})
	}
}

func TestForkAccountCycleObservedRatioExactAmountsAndCompleteness(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, test := range []struct {
		name, total, remaining string
		ratio                  float64
		cost                   int64
		partial                bool
	}{
		{"ordinary", "0.3", "0.27", 0.1, 30_000_000, false},
		{"full ratio", "0.03", "0", 1, 30_000_000, false},
		{"tiny ratio", "0.0001", "0.000099999", 0.00001, 1, false},
		{"partial observed cost", "292.648708", "278.0162726", 0.05, 14_632_435_400, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &forkCycleUsageReader{result: forkCycleUsage(test.cost)}
			if test.partial {
				reader.result.UsageMissingCount, reader.result.PartialCount = 1, 1
				reader.result.UnpricedRequestCount, reader.result.PricingPartialCount = 1, 1
			}
			s := &Service{credentialWindowUsage: reader}
			window := forkCycleWindow("primary", 18000, now+3_600_000, test.ratio)
			window.SourceID = "codex"
			got := s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37, forkCycleObservation(now-60_000, window), now)
			if len(got) != 1 || got[0].Total != test.total || got[0].Remaining != test.remaining ||
				got[0].Method != "observed_ratio" || got[0].Unit != "usd" || !got[0].Estimated || got[0].Complete == test.partial || got[0].SourceID != "codex" {
				t.Fatalf("estimate=%#v", got)
			}
		})
	}
}

func TestForkAccountCycleSkipsMissingInvalidAndExpiredEvidence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	tests := []struct {
		name string
		edit func(*CredentialObservationResponse, *requestlog.CredentialWindowUsage)
	}{
		{"stale", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) { o.State = "stale" }},
		{"no observation time", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) { o.ObservedAtMS = nil }},
		{"unknown state", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			o.Snapshot.QuotaWindows[0].State = "unknown"
		}},
		{"reset reached", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].ResetAtMS = now
		}},
		{"expired", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].ResetAtMS = now - 1
		}},
		{"no reset", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			o.Snapshot.QuotaWindows[0].ResetAtMS = nil
		}},
		{"old cycle observation", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.ObservedAtMS = now - 18_000_000
		}},
		{"future cycle", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].ResetAtMS = now + 18_000_001
		}},
		{"duration overflow", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].WindowSeconds = math.MaxInt64
		}},
		{"negative start", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].WindowSeconds = math.MaxInt64 / 1000
		}},
		{"zero ratio", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = 0
		}},
		{"negative ratio", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = -0.1
		}},
		{"ratio above one", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = 1.1
		}},
		{"NaN", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = math.NaN()
		}},
		{"infinity", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = math.Inf(1)
		}},
		{"amount overflow", func(o *CredentialObservationResponse, _ *requestlog.CredentialWindowUsage) {
			*o.Snapshot.QuotaWindows[0].Utilization = math.SmallestNonzeroFloat64
		}},
		{"no requests", func(_ *CredentialObservationResponse, u *requestlog.CredentialWindowUsage) { u.RequestCount = 0 }},
		{"no positive cost", func(_ *CredentialObservationResponse, u *requestlog.CredentialWindowUsage) {
			u.EstimatedCostNanoUSD = 0
			u.UnpricedRequestCount = 1
		}},
		{"negative cost", func(_ *CredentialObservationResponse, u *requestlog.CredentialWindowUsage) {
			u.EstimatedCostNanoUSD = -1
		}},
		{"data incomplete", func(_ *CredentialObservationResponse, u *requestlog.CredentialWindowUsage) { u.DataComplete = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := forkCycleObservation(now-60_000, forkCycleWindow("primary", 18000, now+3_600_000, 0.64))
			u := forkCycleUsage(10_000_000)
			test.edit(o, &u)
			s := &Service{credentialWindowUsage: &forkCycleUsageReader{result: u}}
			if got := s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37, o, now); got == nil || len(got) != 0 {
				t.Fatalf("invalid evidence produced estimate=%#v", got)
			}
		})
	}
}

func TestForkAccountCycleQueriesOnlyCurrentAccountPeriodsAndMemoizes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	observedAt := now - 60_123
	short := forkCycleWindow("primary", 18000, now+3_600_000, 0.25)
	short.SourceID = "codex"
	duplicate := short
	duplicate.ID = "duplicate"
	weekly := forkCycleWindow("secondary", 604800, now+86_400_000, 0.5)
	model, source, scoped := short, short, short
	model.ID, model.ModelIDs = "model", []string{"spark"}
	source.ID, source.SourceID = "add-on", "codex_bengalfox"
	scoped.ID, scoped.Scope = "surface", "surface"
	otherPeriod := forkCycleWindow("daily", 86400, now+3_600_000, 0.25)
	reader := &forkCycleUsageReader{result: forkCycleUsage(100_000_000)}
	s := &Service{credentialWindowUsage: reader}
	o := forkCycleObservation(observedAt, short, duplicate, weekly, model, source, scoped, otherPeriod)
	before, _ := json.Marshal(o)
	got := s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37, o, now)
	if len(got) != 3 || len(reader.queries) != 2 {
		t.Fatalf("estimates=%#v queries=%#v", got, reader.queries)
	}
	for i, window := range []ObservationQuotaWindow{short, weekly} {
		want := requestlog.CredentialWindowUsageQuery{CredentialID: 37, FromMS: *window.ResetAtMS - *window.WindowSeconds*1000,
			ToMS: observedAt, Source: requestlog.CredentialWindowUsageSourceHourlyStats}
		if reader.queries[i] != want {
			t.Fatalf("query=%#v want=%#v", reader.queries[i], want)
		}
	}
	after, _ := json.Marshal(o)
	if string(before) != string(after) {
		t.Fatal("estimate projection modified shared observation")
	}
	reader.queries = nil
	future := now + 1000
	o.ObservedAtMS = &future
	s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37, o, now)
	if len(reader.queries) != 2 || reader.queries[0].ToMS != now {
		t.Fatalf("future observation did not cap at now: %#v", reader.queries)
	}
	reader.queries, reader.err = nil, errors.New("local query failed")
	if got := s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37, o, now); len(got) != 0 || len(reader.queries) != 2 {
		t.Fatalf("failed queries must not invent zeros or repeat: %#v %#v", got, reader.queries)
	}
}

func TestForkAccountCycleProviderUSDRemainsOnOriginalMonthlyWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	monthly := forkCycleWindow("included_usage", 30*86400, now+86_400_000, 0.25)
	monthly.Scope, monthly.Unit, monthly.SourceID = "credits", "usd", "grok-billing"
	total, used, remaining := 150.0, 37.5, 112.5
	monthly.Limit, monthly.Used, monthly.Remaining = &total, &used, &remaining
	weekly := forkCycleWindow("weekly", 604800, now+3_600_000, 0)
	reader := &forkCycleUsageReader{result: forkCycleUsage(1)}
	s := &Service{credentialWindowUsage: reader}
	got := s.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "grok"}, 7, forkCycleObservation(now-60_000, weekly, monthly), now)
	if len(got) != 1 || got[0].WindowID != "included_usage" || got[0].WindowSeconds != 30*86400 ||
		got[0].SourceID != "grok-billing" || got[0].Total != "150" || got[0].Used != "37.5" || got[0].Remaining != "112.5" ||
		got[0].Estimated || !got[0].Complete || got[0].Method != "provider" || len(reader.queries) != 0 {
		t.Fatalf("provider USD was fabricated/relabelled: %#v %#v", got, reader.queries)
	}
	for _, invalid := range []float64{0, -1, math.Inf(1), math.MaxFloat64} {
		monthly.Limit = &invalid
		if got := s.forkAccountCycleEstimates(t.Context(), models.Group{}, 7, forkCycleObservation(now-60_000, monthly), now); len(got) != 0 {
			t.Fatalf("invalid USD total %g produced %#v", invalid, got)
		}
	}
}

func TestForkAccountCycleActualLocalCostUsesHalfOpenObservationBoundaries(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	// Keep both boundaries within one hour to exercise exact request-log filtering
	// behind the hourly source, including [from,to) and credential isolation.
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	from, observedAt := base.Add(10*time.Minute).UnixMilli(), base.Add(30*time.Minute).UnixMilli()
	now := observedAt + 60_000
	for i, sample := range []struct {
		at         int64
		credential uint
	}{
		{from - 1, 37}, {from, 37}, {observedAt - 1, 37}, {observedAt, 37}, {observedAt + 1, 37}, {from + 1, 38},
	} {
		row := models.RequestLog{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), CompletedAtMS: sample.at,
			CredentialID: sample.credential, Protocol: "openai", Status: "success", StatusCode: 200,
			UsageState: "complete", CostState: "priced", PricingCompleteness: "complete", EstimatedCostNanoUSD: 10_000_000}
		if err := fixture.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	fixture.service.credentialWindowUsage = requestlog.NewService(fixture.db, nil, rpmTestRetention{})
	got := fixture.service.forkAccountCycleEstimates(t.Context(), models.Group{ChannelID: "codex"}, 37,
		forkCycleObservation(observedAt, forkCycleWindow("primary", 18000, from+18_000_000, 0.5)), now)
	if len(got) != 1 || got[0].Used != "0.02" || got[0].Total != "0.04" || got[0].Remaining != "0.02" {
		t.Fatalf("wrong period/observation/credential scope: %#v", got)
	}
}

func TestForkAccountCycleGETIsPageBoundedReadOnlyAndFailureIsOptional(t *testing.T) {
	t.Parallel()
	initControlI18n(t)
	fixture := newServiceFixture(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fixture.service.now = func() time.Time { return now }
	_, credential := createHomeSubscriptionCredential(t, fixture, "cycle-one", "cycle-one", "one@example.com")
	createHomeSubscriptionCredential(t, fixture, "cycle-two", "cycle-two", "two@example.com")
	o := forkCycleObservation(now.Add(-time.Minute).UnixMilli(), forkCycleWindow("primary", 18000, now.Add(time.Hour).UnixMilli(), 0.5))
	body, err := json.Marshal(o.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	stored := models.CredentialObservation{CredentialID: credential.ID, IdentityFingerprint: credential.IdentityFingerprint,
		SchemaVersion: 1, ObservationVersion: 1, State: models.CredentialObservationFresh, ObservedAtMS: o.ObservedAtMS,
		UpdatedAtMS: *o.ObservedAtMS, SnapshotJSON: body}
	if err := fixture.db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.observeSubscriptionAccount = func(context.Context, channel.ID, subscriptionruntime.Credential, subscriptionruntime.Target) (subscriptionruntime.Observation, error) {
		t.Fatal("GET contacted upstream")
		return subscriptionruntime.Observation{}, errors.New("unexpected upstream")
	}
	reader := &forkCycleUsageReader{result: forkCycleUsage(20_000_000)}
	fixture.service.credentialWindowUsage = reader
	beforeRegistry := fixture.registry.Snapshot()
	engine := gin.New()
	NewServer(&config.Config{AuthKey: authTestKey}, fixture.service).RegisterRoutes(engine)
	for _, queryErr := range []error{nil, errors.New("local usage unavailable")} {
		reader.err = queryErr
		response := performHomeRequest(engine, "/api/fork/account-quotas?page=1&page_size=1", authTestKey)
		if response.Code != http.StatusOK {
			t.Fatalf("GET=%d %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data ForkAccountQuotasResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.Total != 2 || len(envelope.Data.Items) != 1 {
			t.Fatalf("page=%#v", envelope.Data)
		}
		estimates := envelope.Data.Items[0].CycleEstimates
		if queryErr == nil && (len(estimates) != 1 || estimates[0].Total != "0.04") || queryErr != nil && (estimates == nil || len(estimates) != 0) {
			t.Fatalf("optional estimates=%#v err=%v", estimates, queryErr)
		}
	}
	second, err := fixture.service.readForkAccountQuotas(t.Context(), forkAccountQuotaQuery{Page: 2, PageSize: 1})
	if err != nil || len(second.Items) != 1 || second.Items[0].CycleEstimates == nil || len(second.Items[0].CycleEstimates) != 0 || len(reader.queries) != 2 {
		t.Fatalf("page two/queries=%#v %#v %v", second, reader.queries, err)
	}
	for _, query := range reader.queries {
		if query.CredentialID != credential.ID {
			t.Fatalf("queried other page: %#v", query)
		}
	}
	var after models.CredentialObservation
	if err := fixture.db.Take(&after, "credential_id = ?", credential.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, after) || !reflect.DeepEqual(beforeRegistry, fixture.registry.Snapshot()) {
		t.Fatal("GET mutated persisted observation or runtime quota")
	}
}
