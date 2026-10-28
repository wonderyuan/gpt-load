package control

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/pricing"
	"gpt-load/internal/requestlog"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

// ForkAccountCycleEstimate is a read-only account-card projection, never a
// pricing/quota input. Amounts are decimal strings in Unit, not Key budgets.
type ForkAccountCycleEstimate struct {
	WindowID      string `json:"window_id"`
	SourceID      string `json:"source_id,omitempty"`
	WindowSeconds int64  `json:"window_seconds"`
	ResetAtMS     int64  `json:"reset_at_ms"`
	Unit          string `json:"unit"`
	Total         string `json:"total"`
	Used          string `json:"used"`
	Remaining     string `json:"remaining"`
	Method        string `json:"method"`
	Estimated     bool   `json:"estimated"`
	Complete      bool   `json:"complete"`
}

func (s *Service) forkAccountCycleEstimates(
	ctx context.Context,
	group models.Group,
	credentialID uint,
	observation *CredentialObservationResponse,
	nowMS int64,
) []ForkAccountCycleEstimate {
	result := []ForkAccountCycleEstimate{}
	if s == nil || credentialID == 0 || observation == nil || observation.Snapshot == nil ||
		observation.State != string(models.CredentialObservationFresh) || observation.ObservedAtMS == nil ||
		*observation.ObservedAtMS <= 0 || validateSafeMilliseconds(*observation.ObservedAtMS) != nil ||
		nowMS <= 0 || validateSafeMilliseconds(nowMS) != nil {
		return result
	}
	observedAtMS := *observation.ObservedAtMS
	glmPro := s.forkAccountGLMProCreditPlan(group, observation.Snapshot.Plan.Name)
	type cachedUsage struct {
		usage requestlog.CredentialWindowUsage
		err   error
	}
	queries := make(map[requestlog.CredentialWindowUsageQuery]cachedUsage)
	for _, window := range observation.Snapshot.QuotaWindows {
		fromMS, toMS, ok := forkAccountCycleBounds(window, observedAtMS, nowMS)
		if !ok {
			continue
		}
		estimate := ForkAccountCycleEstimate{
			WindowID: window.ID, SourceID: window.SourceID,
			WindowSeconds: *window.WindowSeconds, ResetAtMS: *window.ResetAtMS, Complete: true,
		}
		if window.Unit == "usd" && (window.Scope == "account" || window.Scope == "credits") {
			if total, used, remaining, ok := forkAccountProviderUSD(window); ok {
				estimate.Unit, estimate.Method = "usd", "provider"
				estimate.Total, estimate.Used, estimate.Remaining = total, used, remaining
				result = append(result, estimate)
			}
			continue
		}
		if window.Scope != "account" || window.Unit != "percent" ||
			(*window.WindowSeconds != 18000 && *window.WindowSeconds != 604800) {
			continue
		}
		ratio, ok := forkAccountDecimal(window.Utilization)
		if !ok || ratio.Sign() < 0 || ratio.Cmp(big.NewRat(1, 1)) > 0 {
			continue
		}
		// Synthetic GLM credits must never be relabelled as dollar estimates.
		if group.ChannelID == string(channel.ZhipuAI) {
			if !glmPro || window.SourceID != "" && window.SourceID != "zhipuai_coding" {
				continue
			}
			total := int64(12000)
			if *window.WindowSeconds == 604800 {
				total = 60000
			}
			used, ok := forkAccountCycleAmount(new(big.Rat).Mul(big.NewRat(total, 1), ratio))
			if !ok {
				continue
			}
			totalAmount, _ := pricing.ParseUSD(strconv.FormatInt(total, 10))
			estimate.Unit, estimate.Method, estimate.Estimated = "glm_coding_credits", "fixed_plan", true
			estimate.Total, estimate.Used, estimate.Remaining = strconv.FormatInt(total, 10), pricing.FormatUSD(used), pricing.FormatUSD(totalAmount-used)
			result = append(result, estimate)
			continue
		}
		// A credential-wide query cannot price a model/surface/add-on source.
		if ratio.Sign() == 0 || s.credentialWindowUsage == nil || toMS <= fromMS ||
			window.SourceID != "" && window.SourceID != group.ChannelID {
			continue
		}
		query := requestlog.CredentialWindowUsageQuery{
			CredentialID: credentialID, FromMS: fromMS, ToMS: toMS,
			Source: requestlog.CredentialWindowUsageSourceHourlyStats,
		}
		cached, exists := queries[query]
		if !exists {
			cached.usage, cached.err = s.credentialWindowUsage.QueryCredentialWindowUsage(ctx, query)
			queries[query] = cached
		}
		usage := cached.usage
		if cached.err != nil || !usage.DataComplete || usage.RequestCount <= 0 || usage.EstimatedCostNanoUSD <= 0 {
			continue
		}
		used := pricing.NanoUSD(usage.EstimatedCostNanoUSD)
		usedAmount, _ := new(big.Rat).SetString(pricing.FormatUSD(used))
		total, ok := forkAccountCycleAmount(new(big.Rat).Quo(usedAmount, ratio))
		if !ok || total < used {
			continue
		}
		estimate.Unit, estimate.Method, estimate.Estimated = "usd", "observed_ratio", true
		// Partial observed costs remain explicitly approximate, never official totals.
		estimate.Complete = usage.UsageMissingCount == 0 && usage.PartialCount == 0 &&
			usage.UnpricedRequestCount == 0 && usage.PricingPartialCount == 0
		estimate.Total, estimate.Used, estimate.Remaining = pricing.FormatUSD(total), pricing.FormatUSD(used), pricing.FormatUSD(total-used)
		result = append(result, estimate)
	}
	return result
}

// Bounds are the provider's current period, not a rolling local lookback. Stop
// at the observation so local cost and upstream utilization describe one time.
func forkAccountCycleBounds(window ObservationQuotaWindow, observedAtMS, nowMS int64) (int64, int64, bool) {
	if window.ID == "" || len(window.ModelIDs) != 0 ||
		(window.State != "available" && window.State != "exhausted") ||
		window.WindowSeconds == nil || *window.WindowSeconds <= 0 || *window.WindowSeconds > math.MaxInt64/1000 ||
		window.ResetAtMS == nil || *window.ResetAtMS <= nowMS || validateSafeMilliseconds(*window.ResetAtMS) != nil {
		return 0, 0, false
	}
	windowMS := *window.WindowSeconds * 1000
	if windowMS > *window.ResetAtMS {
		return 0, 0, false
	}
	fromMS := *window.ResetAtMS - windowMS
	if observedAtMS < fromMS || observedAtMS >= *window.ResetAtMS || nowMS < fromMS {
		return 0, 0, false
	}
	return fromMS, min(observedAtMS, nowMS, *window.ResetAtMS), true
}

func (s *Service) forkAccountGLMProCreditPlan(group models.Group, plan string) bool {
	if group.ChannelID != string(channel.ZhipuAI) || normalizeGroupConnectionType(group.ConnectionType) != models.ConnectionTypeAPIKey ||
		s.manager == nil || s.channelRegistry == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "pro", "glm-pro":
	default:
		return false
	}
	snapshot := s.manager.Current()
	if snapshot == nil {
		return false
	}
	settings := make(config.Settings)
	if len(group.Overrides) > 0 && json.Unmarshal(group.Overrides, &settings) != nil {
		return false
	}
	resolved, err := state.ResolveGroupRuntimeSettings(snapshot.Settings, settings)
	if err != nil || !resolved.GLMCodingCreditEnabled {
		return false
	}
	target, err := s.channelRegistry.Resolve(channel.ID(group.ChannelID), json.RawMessage(group.Params))
	return err == nil && pricing.IsGLMCodingTarget(target)
}

func forkAccountProviderUSD(window ObservationQuotaWindow) (string, string, string, bool) {
	amounts := make([]string, 0, 3)
	for index, value := range []*float64{window.Limit, window.Used, window.Remaining} {
		ratio, ok := forkAccountDecimal(value)
		if !ok || ratio.Sign() < 0 || index == 0 && ratio.Sign() == 0 {
			return "", "", "", false
		}
		amount, ok := forkAccountCycleAmount(ratio)
		if !ok || index == 0 && amount == 0 {
			return "", "", "", false
		}
		amounts = append(amounts, pricing.FormatUSD(amount))
	}
	return amounts[0], amounts[1], amounts[2], true
}

func forkAccountDecimal(value *float64) (*big.Rat, bool) {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil, false
	}
	return new(big.Rat).SetString(strconv.FormatFloat(*value, 'f', -1, 64))
}

// Round once at nano precision, then use the existing checked decimal parser.
// No float-to-int conversion can overflow or erase a small positive ratio.
func forkAccountCycleAmount(amount *big.Rat) (pricing.NanoUSD, bool) {
	if amount == nil || amount.Sign() < 0 {
		return 0, false
	}
	value, err := pricing.ParseUSD(amount.FloatString(9))
	return value, err == nil
}
