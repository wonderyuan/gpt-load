package control

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/channel"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/platform/version"
	"gpt-load/internal/requestlog"
	"gpt-load/internal/storage/models"
	providerobservation "gpt-load/internal/subscription/providers/observation"
)

// The shared overview is a separate, read-only projection. In particular it
// must not change the AccessKey scopes of home, logs, usage or model queries.
type forkHomeOverviewResponse struct {
	ObservedAtMS int64                      `json:"observed_at_ms"`
	Base         homeResponse               `json:"base"`
	Statistics   forkHomeOverviewStatistics `json:"statistics"`
	Groups       []forkHomeOverviewGroup    `json:"groups"`
	Accounts     forkHomeOverviewAccounts   `json:"accounts"`
	Health       runtimeHealthResponse      `json:"health"`
}

type forkHomeOverviewStatistics struct {
	ObservedAtMS int64                         `json:"observed_at_ms"`
	FromMS       int64                         `json:"from_ms"`
	ToMS         int64                         `json:"to_ms"`
	Summary      forkHomeOverviewCounts        `json:"summary"`
	Series       []forkHomeOverviewSeriesPoint `json:"series"`
}

type forkHomeOverviewCounts struct {
	RequestCount int64 `json:"request_count"`
	FailureCount int64 `json:"failure_count"`
}

type forkHomeOverviewSeriesPoint struct {
	BucketStartMS int64 `json:"bucket_start_ms"`
	forkHomeOverviewCounts
}

type forkHomeOverviewGroup struct {
	ID              uint       `json:"id"`
	Name            string     `json:"name"`
	ChannelID       channel.ID `json:"channel_id"`
	ChannelName     string     `json:"channel_name"`
	ChannelIcon     string     `json:"channel_icon"`
	ChannelMark     string     `json:"channel_mark"`
	Enabled         bool       `json:"enabled"`
	Availability    string     `json:"availability"`
	ModelCount      int64      `json:"model_count"`
	CredentialCount int64      `json:"credential_count"`
}

type forkHomeOverviewAccounts struct {
	ObservedAtMS int64                     `json:"observed_at_ms"`
	Items        []forkHomeOverviewAccount `json:"items"`
}

type forkHomeOverviewAccount struct {
	ChannelID           string                       `json:"channel_id"`
	ChannelName         string                       `json:"channel_name"`
	ChannelMark         string                       `json:"channel_mark"`
	ChannelIcon         string                       `json:"channel_icon"`
	Capabilities        channel.CapabilityDescriptor `json:"capabilities"`
	GroupCount          int                          `json:"group_count"`
	GroupID             *uint                        `json:"group_id"`
	CredentialKey       string                       `json:"credential_key"`
	AvailableGroupCount int                          `json:"available_group_count"`
	Credential          forkHomeOverviewCredential   `json:"credential"`
}

// Use a whitelist rather than embedding CredentialItemResponse: admin-only
// presentation fields (proxy, raw email, error detail) must never grow into the
// shared response when the original credential DTO changes.
type forkHomeOverviewCredential struct {
	CredentialID     uint                         `json:"credential_id"`
	Name             string                       `json:"name"`
	Mask             string                       `json:"mask"`
	ConfiguredStatus string                       `json:"configured_status"`
	EffectiveStatus  string                       `json:"effective_status"`
	AuthState        string                       `json:"auth_state"`
	Account          forkHomeOverviewAccountLabel `json:"account"`
	Observation      *forkHomeOverviewObservation `json:"observation,omitempty"`
}

type forkHomeOverviewAccountLabel struct {
	EmailMask   string `json:"email_mask"`
	ExpiresAtMS *int64 `json:"expires_at_ms,omitempty"`
}

type forkHomeOverviewObservation struct {
	State              string                               `json:"state"`
	ObservationVersion uint64                               `json:"observation_version"`
	ObservedAtMS       *int64                               `json:"observed_at_ms"`
	LastAttemptAtMS    *int64                               `json:"last_attempt_at_ms"`
	LastErrorCode      string                               `json:"last_error_code,omitempty"`
	Snapshot           *forkHomeOverviewObservationSnapshot `json:"snapshot"`
}

type forkHomeOverviewObservationSnapshot struct {
	Plan                  ObservationPlanSummary             `json:"plan_summary"`
	QuotaWindows          []ObservationQuotaWindow           `json:"quota_windows"`
	Credits               *providerobservation.CreditSummary `json:"credits,omitempty"`
	ResetCreditsAvailable *int64                             `json:"reset_credits_available,omitempty"`
	ResetCredits          []ObservationResetCredit           `json:"reset_credits,omitempty"`
}

func (s *Server) handleForkHomeOverview(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		writeServiceError(c, "fork_home_overview", app_errors.ErrBadRequest)
		return
	}
	observedAt := s.now().UTC()
	observedAtMS, err := safeEpochMilliseconds(observedAt)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	startedAtMS, err := safeEpochMilliseconds(s.startedAt)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	ctx := c.Request.Context()
	base, err := s.service.ReadHomeBase(ctx, observedAtMS)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	// This endpoint alone deliberately requests global, fixed-24h statistics.
	// No caller-supplied range, AccessKey ID or other scope is accepted.
	query := requestlog.HomeStatisticsQuery{Range: requestlog.HomeStatistics24H, ObservedAtMS: observedAtMS}
	report, err := s.service.QueryHomeStatistics(ctx, query)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	statistics, err := mapHomeStatisticsResponse(query, report)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	groups, err := s.service.ListModernGroups(ctx)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	accounts, err := s.service.ReadHomeSubscriptionAccounts(ctx)
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	health, err := s.service.RuntimeHealth()
	if err != nil {
		writeServiceError(c, "fork_home_overview", err)
		return
	}
	health.Version = version.Version
	health.UptimeSeconds = max(0, int64(observedAt.Sub(s.startedAt)/time.Second))
	result := forkHomeOverviewResponse{
		ObservedAtMS: observedAtMS,
		Base: homeResponse{
			ServerNowMS: observedAtMS, StartedAtMS: startedAtMS,
			Version: version.Version, HomeBase: base,
		},
		Statistics: forkHomeOverviewStatistics{
			ObservedAtMS: statistics.ObservedAtMS, FromMS: statistics.FromMS, ToMS: statistics.ToMS,
			Summary: forkHomeOverviewCounts{RequestCount: statistics.Summary.RequestCount, FailureCount: statistics.Summary.FailureCount},
			Series:  make([]forkHomeOverviewSeriesPoint, 0, len(statistics.Series)),
		},
		Groups:   make([]forkHomeOverviewGroup, 0, len(groups.Items)),
		Accounts: maskForkHomeOverviewAccounts(accounts),
		Health:   maskForkHomeOverviewHealth(health),
	}
	for _, point := range statistics.Series {
		result.Statistics.Series = append(result.Statistics.Series, forkHomeOverviewSeriesPoint{
			BucketStartMS:          point.BucketStartMS,
			forkHomeOverviewCounts: forkHomeOverviewCounts{RequestCount: point.RequestCount, FailureCount: point.FailureCount},
		})
	}
	// Include disabled groups as well, but never their endpoint, parameters or proxy.
	for _, group := range groups.Items {
		result.Groups = append(result.Groups, forkHomeOverviewGroup{
			ID: group.ID, Name: group.Name, ChannelID: group.ChannelID,
			ChannelName: group.ChannelName, ChannelIcon: group.ChannelIcon, ChannelMark: group.ChannelMark,
			Enabled: group.Enabled, Availability: group.Availability,
			ModelCount: group.ModelCount, CredentialCount: group.Credentials.Total,
		})
	}
	response.SuccessI18n(c, "common.success", result)
}

func maskForkHomeOverviewAccounts(current HomeSubscriptionAccountsResponse) forkHomeOverviewAccounts {
	result := forkHomeOverviewAccounts{
		ObservedAtMS: current.ObservedAtMS,
		Items:        make([]forkHomeOverviewAccount, 0, len(current.Items)),
	}
	for _, item := range current.Items {
		credential := item.Credential
		safe := forkHomeOverviewCredential{
			CredentialID: credential.CredentialID, Name: forkHomeOverviewAlias(credential.Name), Mask: credential.Mask,
			ConfiguredStatus: credential.ConfiguredStatus, EffectiveStatus: credential.EffectiveStatus, AuthState: credential.AuthState,
			Account: forkHomeOverviewAccountLabel{EmailMask: credential.Account.EmailMask, ExpiresAtMS: credential.Account.ExpiresAtMS},
		}
		if observation := credential.Observation; observation != nil {
			safe.Observation = &forkHomeOverviewObservation{
				State: observation.State, ObservationVersion: observation.ObservationVersion,
				ObservedAtMS: observation.ObservedAtMS, LastAttemptAtMS: observation.LastAttemptAtMS,
				LastErrorCode: safeInternalErrorCode(observation.LastErrorCode),
			}
			if snapshot := observation.Snapshot; snapshot != nil {
				safe.Observation.Snapshot = &forkHomeOverviewObservationSnapshot{
					Plan: snapshot.Plan, QuotaWindows: snapshot.QuotaWindows, Credits: snapshot.Credits,
					ResetCreditsAvailable: snapshot.ResetCreditsAvailable, ResetCredits: snapshot.ResetCredits,
				}
			}
		}
		result.Items = append(result.Items, forkHomeOverviewAccount{
			ChannelID: item.ChannelID, ChannelName: item.ChannelName, ChannelMark: item.ChannelMark, ChannelIcon: item.ChannelIcon,
			Capabilities: item.Capabilities, GroupCount: item.GroupCount, GroupID: item.GroupID,
			CredentialKey: item.CredentialKey, AvailableGroupCount: item.AvailableGroupCount, Credential: safe,
		})
	}
	return result
}

func forkHomeOverviewAlias(value string) string {
	if strings.Contains(value, "@") {
		return maskEmail(value)
	}
	return value
}

func forkHomeOverviewHealthIdentity(identity, connectionType string, credentialID uint) string {
	if connectionType == string(models.ConnectionTypeSubscription) || strings.Contains(identity, "@") {
		if masked := maskEmail(identity); masked != "" {
			return masked
		}
		return fmt.Sprintf("Subscription #%d", credentialID)
	}
	// RuntimeHealth already masks API keys; do not attempt to reveal them here.
	return identity
}

func maskForkHomeOverviewHealth(current runtimeHealthResponse) runtimeHealthResponse {
	result := current
	// Detach redacted slices so the existing admin report is not changed.
	result.CooldownCredentials = append([]healthProblemCredentialResponse{}, current.CooldownCredentials...)
	result.BlacklistedCredentials = append([]healthProblemCredentialResponse{}, current.BlacklistedCredentials...)
	result.LowQuotaCredentials = append([]healthQuotaCredentialResponse{}, current.LowQuotaCredentials...)
	result.ExpiringResetCredits = append([]healthExpiringResetCreditResponse{}, current.ExpiringResetCredits...)
	for _, items := range [][]healthProblemCredentialResponse{result.CooldownCredentials, result.BlacklistedCredentials} {
		for index := range items {
			item := &items[index]
			item.Identity = forkHomeOverviewHealthIdentity(item.Identity, item.CredentialConnectionType, item.CredentialID)
			item.CredentialAlias = forkHomeOverviewAlias(item.CredentialAlias)
		}
	}
	for index := range result.LowQuotaCredentials {
		item := &result.LowQuotaCredentials[index]
		item.Identity = forkHomeOverviewHealthIdentity(item.Identity, item.CredentialConnectionType, item.CredentialID)
		item.CredentialAlias = forkHomeOverviewAlias(item.CredentialAlias)
	}
	for index := range result.ExpiringResetCredits {
		item := &result.ExpiringResetCredits[index]
		item.Identity = forkHomeOverviewHealthIdentity(item.Identity, item.CredentialConnectionType, item.CredentialID)
		item.CredentialAlias = forkHomeOverviewAlias(item.CredentialAlias)
	}
	return result
}
