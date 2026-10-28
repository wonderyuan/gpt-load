package modelinfo

type Status string

const (
	StatusReady       Status = "ready"
	StatusStale       Status = "stale"
	StatusUnavailable Status = "unavailable"
)

type Section[T any] struct {
	Status      Status `json:"status"`
	FetchedAtMS *int64 `json:"fetched_at_ms"`
	UpdatedAtMS *int64 `json:"updated_at_ms"`
	Data        *T     `json:"data"`
}

type Report struct {
	ObservedAtMS int64                 `json:"observed_at_ms"`
	Intelligence Section[Intelligence] `json:"intelligence"`
	Speed        Section[Speed]        `json:"speed"`
	Quota        Section[Quota]        `json:"quota"`
}

type Intelligence struct {
	Batch    string        `json:"batch"`
	TestedAt string        `json:"tested_at"`
	MaxScore float64       `json:"max_score"`
	Efforts  []string      `json:"efforts"`
	Models   []ScoreModel  `json:"models"`
	Pareto   []ParetoPoint `json:"pareto"`
}

type ScoreModel struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Results []ScoreResult `json:"results"`
}

type ScoreResult struct {
	Effort          string   `json:"effort"`
	Score           *float64 `json:"score"`
	CostUSD         *float64 `json:"cost_usd"`
	DurationSeconds *float64 `json:"duration_seconds"`
}

type ParetoPoint struct {
	ID              string  `json:"id"`
	ModelID         string  `json:"model_id"`
	Model           string  `json:"model"`
	Effort          string  `json:"effort"`
	Score           float64 `json:"score"`
	CostUSD         float64 `json:"cost_usd"`
	DurationMinutes float64 `json:"duration_minutes"`
	CompositeCost   float64 `json:"composite_cost"`
}

type Speed struct {
	WindowMinutes int          `json:"window_minutes"`
	Models        []SpeedModel `json:"models"`
}

type SpeedModel struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	TokensPerSecond *float64     `json:"tokens_per_second"`
	Requests        int64        `json:"requests"`
	Devices         int64        `json:"devices"`
	Harnesses       []Harness    `json:"harnesses"`
	History         []SpeedPoint `json:"history"`
}

type Harness struct {
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
}

type SpeedPoint struct {
	AtMS  int64    `json:"at_ms"`
	Value *float64 `json:"value"`
}

type Quota struct {
	RecordedAt string      `json:"recorded_at"`
	Plans      []QuotaPlan `json:"plans"`
}

type QuotaPlan struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	MonthlyUSD float64      `json:"monthly_usd"`
	Models     []QuotaModel `json:"models"`
}

type QuotaModel struct {
	Name         string   `json:"name"`
	WeeklyTokens *float64 `json:"weekly_tokens"`
	APIValueUSD  *float64 `json:"api_value_usd"`
}
