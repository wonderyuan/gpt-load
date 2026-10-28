package apikeyobservation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	providerobservation "gpt-load/internal/subscription/providers/observation"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

const (
	zhipuResetListURL = "https://www.bigmodel.cn/api/biz/customer-package-reset/list?targetType=PERSONAL"
	zhipuResetUseURL  = "https://www.bigmodel.cn/api/biz/customer-package-reset/use"
)

// Only this non-secret selection is persisted. Neither record IDs nor grant types
// are projected into the browser's observation snapshot.
type zhipuResetSelection struct {
	ResetType  string `json:"resetType"`
	RecordID   int64  `json:"recordId"`
	GrantType  string `json:"grantType"`
	TargetType string `json:"targetType"`
}

type zhipuResetCard struct {
	RecordID   int64  `json:"recordId"`
	GrantType  string `json:"grantType"`
	ExpireTime string `json:"expireTime"`
	Available  *bool  `json:"available"`
}

type zhipuAvailableReset struct {
	selection zhipuResetSelection
	expiresMS int64
	kind      string
}

func (ZhipuObserver) ResetTypes() []string { return []string{"five_hour", "week"} }

func (observer ZhipuObserver) PrepareResetCredit(ctx context.Context, request Request, kind string) (json.RawMessage, error) {
	if kind != "five_hour" && kind != "week" {
		return nil, errors.New("unsupported GLM reset type")
	}
	cards, err := observer.listResetCredits(ctx, request)
	if err != nil {
		return nil, err
	}
	for _, card := range cards {
		if card.kind == kind {
			return json.Marshal(card.selection)
		}
	}
	return nil, &subscriptionruntime.UpstreamHTTPError{StatusCode: http.StatusConflict}
}

func (observer ZhipuObserver) ConsumeResetCredit(ctx context.Context, request Request, reset subscriptionruntime.ResetCreditRequest) (subscriptionruntime.ResetCreditResult, error) {
	var selection zhipuResetSelection
	if json.Unmarshal(reset.Data, &selection) != nil || !validZhipuSelection(selection) {
		return subscriptionruntime.ResetCreditResult{}, errors.New("invalid stored GLM reset selection")
	}
	id, err := uuid.Parse(reset.ID)
	if err != nil || id == uuid.Nil || id.String() != reset.ID {
		return subscriptionruntime.ResetCreditResult{}, errors.New("invalid GLM reset request ID")
	}
	// Never re-list here: an uncertain redemption must retain its original card.
	body, err := json.Marshal(struct {
		zhipuResetSelection
		RequestID string `json:"requestId"`
	}{selection, reset.ID})
	if err != nil {
		return subscriptionruntime.ResetCreditResult{}, errors.New("build GLM reset body")
	}
	raw, err := observer.zhipuRequest(ctx, request, http.MethodPost, zhipuResetUseURL, body)
	if err != nil {
		return subscriptionruntime.ResetCreditResult{}, err
	}
	if err := validateZhipuResetEnvelope(raw); err != nil {
		return subscriptionruntime.ResetCreditResult{}, err
	}
	windows := 1
	if selection.ResetType == "WEEK" {
		// The official weekly card also resets the five-hour window.
		windows = 2
	}
	return subscriptionruntime.ResetCreditResult{Status: "succeeded", WindowsReset: windows}, nil
}

func validZhipuSelection(selection zhipuResetSelection) bool {
	return (selection.ResetType == "FIVE_HOUR" || selection.ResetType == "WEEK") &&
		selection.TargetType == "PERSONAL" && selection.RecordID > 0 && selection.RecordID <= 9007199254740991 &&
		strings.TrimSpace(selection.GrantType) != "" && len(selection.GrantType) <= 128
}

func (observer ZhipuObserver) listResetCredits(ctx context.Context, request Request) ([]zhipuAvailableReset, error) {
	raw, err := observer.zhipuRequest(ctx, request, http.MethodGet, zhipuResetListURL, nil)
	if err != nil {
		return nil, err
	}
	return normalizeZhipuResetList(raw, time.Now())
}

func normalizeZhipuResetList(raw []byte, now time.Time) ([]zhipuAvailableReset, error) {
	if err := validateZhipuResetEnvelope(raw); err != nil {
		return nil, err
	}
	var envelope struct {
		Data struct {
			TargetType     string           `json:"targetType"`
			FiveHourResets []zhipuResetCard `json:"fiveHourResets"`
			WeekResets     []zhipuResetCard `json:"weekResets"`
		} `json:"data"`
	}
	invalid := subscriptionruntime.ErrObservationPayloadInvalid
	if json.Unmarshal(raw, &envelope) != nil || envelope.Data.TargetType != "PERSONAL" ||
		envelope.Data.FiveHourResets == nil || envelope.Data.WeekResets == nil {
		return nil, invalid
	}
	cards := make([]zhipuAvailableReset, 0)
	seen := make(map[zhipuResetSelection]bool)
	for _, group := range []struct {
		kind, resetType string
		cards           []zhipuResetCard
	}{
		{"five_hour", "FIVE_HOUR", envelope.Data.FiveHourResets},
		{"week", "WEEK", envelope.Data.WeekResets},
	} {
		for _, card := range group.cards {
			if card.Available == nil {
				return nil, invalid
			}
			if !*card.Available {
				continue
			}
			selection := zhipuResetSelection{ResetType: group.resetType, RecordID: card.RecordID, GrantType: card.GrantType, TargetType: "PERSONAL"}
			expires, ok := zhipuResetExpiry(card.ExpireTime)
			if !validZhipuSelection(selection) || !ok || seen[selection] {
				return nil, invalid
			}
			seen[selection] = true
			if expires <= now.UnixMilli() {
				continue
			}
			cards = append(cards, zhipuAvailableReset{selection: selection, expiresMS: expires, kind: group.kind})
		}
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].expiresMS == cards[j].expiresMS {
			return cards[i].selection.RecordID < cards[j].selection.RecordID
		}
		return cards[i].expiresMS < cards[j].expiresMS
	})
	return cards, nil
}

func zhipuResetExpiry(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		// The official API returns China-local timestamps with no offset.
		parsed, err = time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("Asia/Shanghai", 8*60*60))
	}
	return parsed.UnixMilli(), err == nil && parsed.UnixMilli() > 0
}

func projectZhipuResetCredits(cards []zhipuAvailableReset) (int64, []providerobservation.ResetCredit) {
	credits := make([]providerobservation.ResetCredit, 0, len(cards))
	for _, card := range cards {
		expiry, available := card.expiresMS, true
		credits = append(credits, providerobservation.ResetCredit{Kind: card.kind, Available: &available, ExpiresAtMS: &expiry})
	}
	return int64(len(credits)), credits
}

// Business messages are used only to classify known rejections; they are never
// returned/logged. Malformed/contradictory success responses remain unknown.
func validateZhipuResetEnvelope(raw []byte) error {
	var envelope struct {
		Success *bool           `json:"success"`
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Message string          `json:"message"`
	}
	invalid := subscriptionruntime.ErrObservationPayloadInvalid
	if json.Unmarshal(raw, &envelope) != nil || envelope.Success == nil {
		return invalid
	}
	code, ok := quotaNumber(envelope.Code)
	if !ok {
		return invalid
	}
	if *envelope.Success && code == 200 {
		return nil
	}
	if *envelope.Success || code == 200 {
		return invalid
	}
	status := http.StatusBadRequest
	if code >= 500 {
		return invalid
	}
	switch {
	case code == 401 || code == 403:
		status = int(code)
	case code == 404 || code == 409 || envelope.Msg == "指定的重置次数不可用，请刷新后重试" || envelope.Message == "指定的重置次数不可用，请刷新后重试":
		status = http.StatusConflict
	case code == 429:
		status = http.StatusTooManyRequests
	}
	return &subscriptionruntime.UpstreamHTTPError{StatusCode: status}
}

func (observer ZhipuObserver) zhipuRequest(ctx context.Context, request Request, method, endpoint string, body []byte) ([]byte, error) {
	if !observer.SupportsTarget(request.Target) {
		return nil, errors.New("unsupported GLM coding target")
	}
	key, _ := request.Credential.Value("api_key")
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("missing GLM credential")
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	factory := observer.client
	if factory == nil {
		factory = subscriptionruntime.HTTPClient
	}
	client, err := factory(ctx)
	if err != nil {
		return nil, errors.New("initialize GLM transport")
	}
	if client == nil {
		return nil, errors.New("missing GLM transport")
	}
	bounded := *client
	bounded.Timeout = quotaTimeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build GLM request")
	}
	req.Header.Set("Authorization", strings.TrimSpace(key))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := bounded.Do(req)
	if err != nil {
		return nil, errors.New("GLM request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode < 400 {
			return nil, subscriptionruntime.ErrObservationPayloadInvalid
		}
		return nil, &subscriptionruntime.UpstreamHTTPError{StatusCode: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody+1))
	if err != nil || len(raw) > maxQuotaBody {
		return nil, subscriptionruntime.ErrObservationPayloadInvalid
	}
	return raw, nil
}
