package control

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/pricing"
	"gpt-load/internal/state"
	"gpt-load/internal/storage/models"
)

type forkKeyQuotaQuery struct {
	Page     int
	PageSize int
}

type ForkKeyQuotaGroup struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Enabled     bool   `json:"enabled"`
}

type ForkKeyQuotaAccounting struct {
	Unit                   string `json:"unit"`
	InternalUnitsPerCredit string `json:"internal_units_per_credit,omitempty"`
	// This describes potential routing under current configuration, not the
	// historical composition of the shared native estimated-cost ledger.
	Basis string `json:"basis"`
}

type ForkKeyQuotaItem struct {
	ID          uint                  `json:"id"`
	Name        string                `json:"name"`
	MaskedKey   string                `json:"masked_key"`
	Status      state.AccessKeyStatus `json:"status"`
	ExpiresAtMS *int64                `json:"expires_at_ms"`
	// GroupIDs is the original configured scope; only here does [] mean all.
	GroupIDs          []uint                   `json:"group_ids"`
	PotentialGroupIDs []uint                   `json:"potential_group_ids"`
	EffectiveGroupIDs []uint                   `json:"effective_group_ids"`
	Accounting        ForkKeyQuotaAccounting   `json:"accounting"`
	CostLimitRules    []AccessKeyCostLimitRule `json:"cost_limit_rules"`
	CostLimitStatus   AccessKeyCostLimitStatus `json:"cost_limit_status"`
}

type ForkKeyQuotasResponse struct {
	ObservedAtMS int64               `json:"observed_at_ms"`
	Groups       []ForkKeyQuotaGroup `json:"groups"`
	Items        []ForkKeyQuotaItem  `json:"items"`
	Total        int64               `json:"total"`
	Page         int                 `json:"page"`
	PageSize     int                 `json:"page_size"`
}

type forkKeyQuotaRow struct {
	Metadata accessKeyMetadataRow `gorm:"embedded"`
	KeyHash  string
}

func parseForkKeyQuotaQuery(raw string) (forkKeyQuotaQuery, error) {
	query := forkKeyQuotaQuery{Page: 1, PageSize: 50}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, app_errors.ErrBadRequest
	}
	for key, entries := range values {
		if len(entries) != 1 || key != "page" && key != "page_size" {
			return query, app_errors.ErrBadRequest
		}
		value, err := parseCanonicalSafeUint(entries[0])
		if err != nil || value == 0 || value > 1_000_000 || key == "page_size" && value > 100 {
			return query, app_errors.ErrBadRequest
		}
		if key == "page" {
			query.Page = int(value)
		} else {
			query.PageSize = int(value)
		}
	}
	return query, nil
}

func (s *Server) handleForkKeyQuotas(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query, err := parseForkKeyQuotaQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeServiceError(c, "fork_key_quotas", err)
		return
	}
	result, err := s.service.readForkKeyQuotas(c.Request.Context(), query)
	if err != nil {
		writeServiceError(c, "fork_key_quotas", err)
		return
	}
	response.SuccessI18n(c, "common.success", result)
}

// Only bounded Key metadata and the small configuration group table are read.
// No ciphertext, request history, credential decoder or upstream is involved.
func (s *Service) readForkKeyQuotas(ctx context.Context, query forkKeyQuotaQuery) (ForkKeyQuotasResponse, error) {
	if s == nil || s.db == nil || s.manager == nil || s.channelRegistry == nil || s.accessQuota == nil || s.now == nil {
		return ForkKeyQuotasResponse{}, app_errors.ErrInternalServer
	}
	if query.Page < 1 || query.Page > 1_000_000 || query.PageSize < 1 || query.PageSize > 100 {
		return ForkKeyQuotasResponse{}, app_errors.ErrBadRequest
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ForkKeyQuotasResponse{}, err
	}
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	observedAt := s.now().UTC()
	observedAtMS, err := safeEpochMilliseconds(observedAt)
	if err != nil {
		return ForkKeyQuotasResponse{}, err
	}
	result := ForkKeyQuotasResponse{
		ObservedAtMS: observedAtMS, Groups: []ForkKeyQuotaGroup{}, Items: []ForkKeyQuotaItem{},
		Page: query.Page, PageSize: query.PageSize,
	}
	var readErr error
	// writeMu keeps persisted configuration and the published snapshot aligned.
	// Do not hold the publication lock over SQL or configuration decoding.
	snapshot := s.manager.Current()
	func() bool {
		if snapshot == nil {
			readErr = app_errors.ErrInternalServer
			return false
		}
		var rows []forkKeyQuotaRow
		var groups []models.Group
		var rules []models.AccessKeyCostLimitRule
		readErr = s.withReadSnapshot(ctx, func(tx *gorm.DB) error {
			if err := tx.Model(&models.AccessKey{}).Count(&result.Total).Error; err != nil {
				return err
			}
			if err := tx.Select("id", "name", "channel_id", "connection_type", "params", "models", "overrides", "enabled").
				Order("id ASC").Find(&groups).Error; err != nil {
				return err
			}
			offset := int64(query.Page-1) * int64(query.PageSize)
			if offset >= result.Total {
				return nil
			}
			if err := tx.Model(&models.AccessKey{}).
				Select("id", "name", "key_prefix", "key_suffix", "key_hash", "status", "filters", "rpm_limit", "concurrency_limit", "price_multiplier_micros", "expires_at_ms", "created_at_ms", "updated_at_ms", "record_request_body").
				Order("id ASC").Offset(int(offset)).Limit(query.PageSize).Scan(&rows).Error; err != nil {
				return err
			}
			ids := make([]uint, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, row.Metadata.ID)
			}
			if len(ids) == 0 {
				return nil
			}
			return tx.Select("id", "access_key_id", "kind", "limit_nano_usd", "period_seconds", "rule_revision").Where("access_key_id IN ?", ids).
				Order("access_key_id ASC, CASE WHEN kind = 'total' THEN 0 ELSE 1 END ASC, period_seconds ASC, id ASC").Find(&rules).Error
		})
		if readErr != nil {
			readErr = app_errors.ParseDBError(readErr)
			return false
		}
		configured, accountingKnown, groupErr := s.forkKeyQuotaGroups(groups, snapshot, &result)
		if groupErr != nil {
			readErr = groupErr
			return false
		}
		rulesByKey := make(map[uint][]accessquota.Rule)
		for _, row := range rules {
			rulesByKey[row.AccessKeyID] = append(rulesByKey[row.AccessKeyID], accessquota.Rule{
				ID: row.ID, Revision: row.RuleRevision, Kind: accessquota.Kind(row.Kind), LimitNanoUSD: row.LimitNanoUSD, PeriodSeconds: row.PeriodSeconds,
			})
		}
		for _, row := range rows {
			metadata, err := mapAccessKeyMetadataRow(row.Metadata)
			if err != nil {
				readErr = err
				return false
			}
			key, exists := snapshot.AccessKeysByID[metadata.ID]
			definitions := rulesByKey[metadata.ID]
			if !exists || !forkKeyQuotaMetadataMatches(row, metadata, key, snapshot) || !slices.Equal(definitions, key.CostLimitRules) {
				readErr = forkKeyQuotaMismatch(metadata.ID)
				return false
			}
			view := s.accessQuota.Snapshot(metadata.ID, observedAt)
			if len(view.Rules) != len(definitions) {
				readErr = forkKeyQuotaMismatch(metadata.ID)
				return false
			}
			for i, rule := range view.Rules {
				if rule.Rule != definitions[i] {
					readErr = forkKeyQuotaMismatch(metadata.ID)
					return false
				}
			}
			status := mapAccessKeyCostLimitStatus(view)
			potential := accessibleHomeGroups(configured, key)
			item := ForkKeyQuotaItem{
				ID: metadata.ID, Name: metadata.Name, MaskedKey: metadata.MaskedKey, Status: metadata.Status, ExpiresAtMS: metadata.ExpiresAtMS,
				GroupIDs: slices.Clone(metadata.Filters.Groups), PotentialGroupIDs: sortedForkQuotaGroupIDs(potential),
				EffectiveGroupIDs: sortedForkQuotaGroupIDs(accessibleHomeGroups(snapshot, key)),
				Accounting:        forkKeyQuotaAccounting(potential, configured, accountingKnown, key),
				CostLimitRules:    costLimitDefinitionsFromStatus(status), CostLimitStatus: status,
			}
			result.Items = append(result.Items, item)
		}
		return true
	}()
	if err := ctx.Err(); err != nil {
		return ForkKeyQuotasResponse{}, err
	}
	if readErr != nil {
		return ForkKeyQuotasResponse{}, readErr
	}
	return result, nil
}

// Build a configuration-only view including disabled groups, so the exact home
// group/protocol/external-model matcher can also describe potential scope.
func (s *Service) forkKeyQuotaGroups(rows []models.Group, snapshot *state.ConfigSnapshot, result *ForkKeyQuotasResponse) (*state.ConfigSnapshot, map[uint]bool, error) {
	configured := &state.ConfigSnapshot{Groups: make(map[uint]state.GroupView, len(rows))}
	known := make(map[uint]bool, len(rows))
	if len(rows) != len(snapshot.GroupCatalog) {
		return nil, nil, forkKeyQuotaMismatch(0)
	}
	for _, row := range rows {
		catalog, exists := snapshot.GroupCatalog[row.ID]
		if !exists || catalog.Name != row.Name || string(catalog.ChannelID) != row.ChannelID || catalog.Enabled != row.Enabled || catalog.ConnectionType != string(normalizeGroupConnectionType(row.ConnectionType)) {
			return nil, nil, forkKeyQuotaMismatch(0)
		}
		descriptor, exists := s.channelRegistry.Get(channel.ID(row.ChannelID))
		if !exists {
			return nil, nil, forkKeyQuotaMismatch(0)
		}
		var groupModels []state.ModelConfig
		if err := decodeGroupDiscoveryJSON(row.Models, &groupModels); err != nil {
			return nil, nil, forkKeyQuotaMismatch(0)
		}
		settings := make(config.Settings)
		if len(row.Overrides) > 0 {
			if err := decodeGroupDiscoveryJSON(row.Overrides, &settings); err != nil {
				return nil, nil, forkKeyQuotaMismatch(0)
			}
		}
		resolved, err := state.ResolveGroupRuntimeSettings(snapshot.Settings, settings)
		known[row.ID] = err == nil
		group := state.GroupView{ID: row.ID, Models: groupModels, ClientProtocols: descriptor.ClientProtocols, GLMCodingCreditEnabled: resolved.GLMCodingCreditEnabled}
		if group.GLMCodingCreditEnabled {
			target, targetErr := s.channelRegistry.Resolve(channel.ID(row.ChannelID), json.RawMessage(row.Params))
			known[row.ID] = known[row.ID] && targetErr == nil && pricing.IsGLMCodingTarget(target) && normalizeGroupConnectionType(row.ConnectionType) == models.ConnectionTypeAPIKey
		}
		if row.Enabled {
			active, exists := snapshot.Groups[row.ID]
			if !exists || !slices.Equal(active.Models, group.Models) || !slices.Equal(active.ClientProtocols, group.ClientProtocols) || known[row.ID] && active.GLMCodingCreditEnabled != group.GLMCodingCreditEnabled {
				return nil, nil, forkKeyQuotaMismatch(0)
			}
		}
		configured.Groups[row.ID] = group
		result.Groups = append(result.Groups, ForkKeyQuotaGroup{ID: row.ID, Name: row.Name, ChannelID: row.ChannelID, ChannelName: descriptor.Name, Enabled: row.Enabled})
	}
	return configured, known, nil
}

func forkKeyQuotaMetadataMatches(row forkKeyQuotaRow, metadata AccessKeyMetadata, key state.AccessKeyView, snapshot *state.ConfigSnapshot) bool {
	if key.ID != metadata.ID || key.Status != metadata.Status || key.Name != metadata.Name || key.KeyPrefix != row.Metadata.KeyPrefix || key.KeySuffix != row.Metadata.KeySuffix ||
		!equalForkQuotaOptionalInt64(key.ExpiresAtMS, metadata.ExpiresAtMS) || !equalForkQuotaOptionalInt64(key.ConcurrencyLimit, metadata.ConcurrencyLimit) ||
		key.RPMLimit != metadata.RPMLimit || key.PriceMultiplier != priceMultiplierFromStorage(row.Metadata.PriceMultiplierMicros) || key.RecordRequestBody != metadata.RecordRequestBody {
		return false
	}
	return forkKeyQuotaFiltersMatch(metadata.Filters, key.Filters) && (key.Status != state.AccessKeyStatusActive || row.KeyHash != "" && snapshot.AccessKeysByHash[row.KeyHash].ID == key.ID)
}

func forkKeyQuotaFiltersMatch(filters AccessKeyFilters, runtime state.FilterSet) bool {
	if len(filters.Groups) != len(runtime.Groups) || len(filters.Protocols) != len(runtime.Protocols) || len(filters.Models) != len(runtime.Models) {
		return false
	}
	for _, id := range filters.Groups {
		if _, ok := runtime.Groups[id]; !ok {
			return false
		}
	}
	for _, value := range filters.Protocols {
		if _, ok := runtime.Protocols[value]; !ok {
			return false
		}
	}
	for _, value := range filters.Models {
		if _, ok := runtime.Models[value]; !ok {
			return false
		}
	}
	return true
}

func equalForkQuotaOptionalInt64(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func forkKeyQuotaAccounting(scope map[uint]struct{}, configured *state.ConfigSnapshot, known map[uint]bool, key state.AccessKeyView) ForkKeyQuotaAccounting {
	result := ForkKeyQuotaAccounting{Unit: "unknown", Basis: "current_config"}
	for id := range key.Filters.Groups {
		if _, exists := configured.Groups[id]; !exists {
			return result
		}
	}
	if len(scope) == 0 {
		return result
	}
	credit, usd := false, false
	for id := range scope {
		if !known[id] {
			return result
		}
		if configured.Groups[id].GLMCodingCreditEnabled {
			credit = true
		} else {
			usd = true
		}
	}
	switch {
	case credit && usd:
		result.Unit = "mixed"
	case credit:
		result.Unit, result.InternalUnitsPerCredit = "glm_coding_credits", "0.01"
	case usd:
		result.Unit = "usd"
	}
	return result
}

func sortedForkQuotaGroupIDs(groups map[uint]struct{}) []uint {
	ids := slices.Collect(maps.Keys(groups))
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if ids == nil {
		return []uint{}
	}
	return ids
}

func forkKeyQuotaMismatch(id uint) error {
	return fmt.Errorf("fork key quotas %d: runtime configuration mismatch: %w", id, app_errors.ErrInternalServer)
}
