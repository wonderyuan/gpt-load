package bifrost

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"

	"gpt-load/internal/execution"
	"gpt-load/internal/health"
)

func TestPermissionErrorNativeAndConvertedMappings(t *testing.T) {
	const secret = "fixture-provider-secret"
	const body = `{"error":{"type":"permission_error","code":"permission_denied","message":"permission denied token=fixture-provider-secret"}}`
	const summary = "permission denied token=[REDACTED]"
	var payload struct {
		Error schemas.ErrorField `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	sdkError := &schemas.BifrostError{
		StatusCode: schemas.Ptr(http.StatusForbidden),
		Type:       schemas.Ptr("error"), // The structured inner type must win.
		Error:      &payload.Error,
	}
	secrets := []string{secret}
	for _, test := range []struct {
		name     string
		mapError func() execution.AttemptResult
	}{
		{
			name: "native passthrough",
			mapError: func() execution.AttemptResult {
				evidence := passthroughHTTPError(http.StatusForbidden, nil, []byte(body), secrets)
				return execution.AttemptResult{
					DispatchState: execution.DispatchMaybeSent, ResponseStarted: true,
					StatusCode: http.StatusForbidden, Error: evidence, Body: encodeSafeErrorBody(evidence),
				}
			},
		},
		{name: "native SDK unary", mapError: func() execution.AttemptResult {
			return unaryErrorResult(sdkError, nil, secrets)
		}},
		{name: "converted SDK unary", mapError: func() execution.AttemptResult {
			return convertedUnaryErrorResult(sdkError, nil, secrets)
		}},
		{name: "native SDK stream rejection", mapError: func() execution.AttemptResult {
			result := streamErrorResult(sdkError, nil, secrets, false, 0, nil, "", nil)
			return execution.AttemptResult{DispatchState: result.DispatchState, ResponseStarted: result.ResponseStarted,
				StatusCode: result.StatusCode, Error: result.Error}
		}},
		{name: "converted SDK stream rejection", mapError: func() execution.AttemptResult {
			result := convertedStreamErrorResult(sdkError, nil, secrets, false, 0, nil, "", nil)
			return execution.AttemptResult{DispatchState: result.DispatchState, ResponseStarted: result.ResponseStarted,
				StatusCode: result.StatusCode, Error: result.Error}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := test.mapError()
			if result.DispatchState != execution.DispatchMaybeSent || !result.ResponseStarted ||
				result.StatusCode != http.StatusForbidden || result.Error == nil {
				t.Fatalf("mapped result=%+v", result)
			}
			evidence := result.Error
			if evidence.Kind != execution.ErrorKindHTTP || evidence.StatusCode != http.StatusForbidden ||
				evidence.Type != "permission_error" || evidence.Code != "permission_denied" ||
				evidence.OriginHint != execution.ErrorOriginUpstream || evidence.Summary != summary {
				t.Fatalf("mapped evidence=%+v", evidence)
			}
			before := evidence.Clone()
			got := health.JudgeExecution(health.ExecutionAttempt{
				DispatchState: result.DispatchState, StatusCode: result.StatusCode, Evidence: evidence,
			}, health.DecisionContext{Method: http.MethodPost, Operation: execution.OperationChatCompletion, CredentialRefreshable: true})
			if got.Retry != health.RetryNone || got.Effect != health.EffectNone ||
				got.Scope != execution.ErrorScopeRequest || !got.CooldownUntil.IsZero() || got.QuotaCooldown {
				t.Fatalf("mapped permission error decision=%+v", got)
			}
			if result.StatusCode != http.StatusForbidden || !reflect.DeepEqual(*evidence, before) {
				t.Fatal("decision changed status or sanitized evidence")
			}
			if len(result.Body) > 0 {
				var safe struct {
					Error struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(result.Body, &safe); err != nil {
					t.Fatal(err)
				}
				if safe.Error.Type != "permission_error" || safe.Error.Message != summary || strings.Contains(string(result.Body), secret) {
					t.Fatalf("sanitized body=%s", result.Body)
				}
			}
		})
	}
}

func TestPermissionErrorCommittedStreamMappingPreservesOuterStatus(t *testing.T) {
	sdkError := &schemas.BifrostError{
		StatusCode: schemas.Ptr(http.StatusForbidden),
		Error:      &schemas.ErrorField{Type: schemas.Ptr("permission_error"), Message: "permission denied"},
	}
	for _, converted := range []bool{false, true} {
		mapper := streamErrorResult
		name := "native"
		if converted {
			mapper, name = convertedStreamErrorResult, "converted"
		}
		t.Run(name, func(t *testing.T) {
			result := mapper(sdkError, nil, nil, true, http.StatusOK, nil, "", nil)
			if result.StatusCode != http.StatusOK || result.Error == nil ||
				result.Error.StatusCode != 0 || result.Error.Type != "permission_error" {
				t.Fatalf("committed stream mapping=%+v", result)
			}
			got := health.JudgeExecution(health.ExecutionAttempt{
				DispatchState: result.DispatchState, StatusCode: result.StatusCode,
				Evidence: result.Error, DownstreamCommitted: true,
			}, health.DecisionContext{Method: http.MethodPost, Operation: execution.OperationChatCompletion})
			if got.Retry != health.RetryNone || got.Effect != health.EffectNone || got.RuleID == "request.permission_denied" {
				t.Fatalf("committed stream decision=%+v", got)
			}
		})
	}
}
