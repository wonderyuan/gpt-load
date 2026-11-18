package health

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/execution"
)

func TestPermissionError403TerminatesWithoutHealthEffects(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		evidence  int
		dispatch  execution.DispatchState
		origin    execution.ErrorOrigin
		hint      execution.FailureHint
		committed bool
	}{
		{name: "upstream response", status: 403, evidence: 403, origin: execution.ErrorOriginUpstream},
		{name: "status fallback", evidence: 403},
		{name: "response status takes precedence", status: 403, evidence: 401},
		{name: "implicit upstream origin", status: 403},
		{name: "unsent handshake", status: 403, evidence: 403, dispatch: execution.DispatchNotSent},
		{name: "refresh hint cannot refresh", status: 403, hint: execution.FailureHintRefreshRequired},
		{name: "invalid credential hint cannot penalize", status: 403, hint: execution.FailureHintInvalidCredential},
		{name: "rate limit hint cannot cool down", status: 403, hint: execution.FailureHintRateLimited},
		{name: "committed stream", status: 403, evidence: 403, committed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatch := test.dispatch
			if dispatch == "" {
				dispatch = execution.DispatchMaybeSent
			}
			evidence := &execution.ErrorEvidence{
				Kind: execution.ErrorKindHTTP, Type: "permission_error", StatusCode: test.evidence,
				OriginHint: test.origin, Hint: test.hint, ScopeHint: execution.ErrorScopeCredential,
				Code: "permission_denied", Summary: "permission denied token=[REDACTED]",
				ReplaySafety: execution.ReplaySafetyRejectedBeforeProcessing,
				RetryAfter:   time.Minute, Header: http.Header{"Retry-After": {"60"}},
			}
			before := evidence.Clone()
			attempt := ExecutionAttempt{
				DispatchState: dispatch, StatusCode: test.status, Evidence: evidence,
				DownstreamCommitted: test.committed, Now: time.Unix(100, 0),
			}
			// Even a configured retry/credential effect must not widen this request-only denial.
			for _, effect := range []Effect{EffectNone, EffectRecordCredentialFailure, EffectCooldownCredential, EffectCooldownModel, EffectSkipGroup} {
				rule := ErrorRule{StatusCodes: []int{403}, Retry: RetryNextCandidate, Effect: effect}
				if effect == EffectCooldownCredential || effect == EffectCooldownModel {
					rule.CooldownSeconds = 60
				}
				rules, err := CompileErrorRules([]ErrorRule{rule}, "group")
				if err != nil {
					t.Fatal(err)
				}
				got := JudgeExecution(attempt, DecisionContext{
					Method: http.MethodPost, Operation: execution.OperationChatCompletion,
					CredentialRefreshable: true, ErrorRules: rules,
				})
				want := decision(FailureCategoryClientError, execution.ErrorOriginUpstream,
					execution.ErrorScopeRequest, RetryNone, EffectNone, "request.permission_denied")
				if got != want || got.ShouldRetry() || got.LegacyAction() != ActionTerminate {
					t.Fatalf("custom effect=%s: decision=%+v, want %+v", effect, got, want)
				}
				if err := got.Validate(); err != nil {
					t.Fatal(err)
				}
				if attempt.StatusCode != test.status || !reflect.DeepEqual(*evidence, before) {
					t.Fatal("decision changed upstream status or sanitized evidence")
				}
			}
		})
	}
}

func TestPermissionErrorGuardDoesNotMatchOtherFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		evidence  execution.ErrorEvidence
		committed bool
		retry     RetryDirective
		effect    Effect
	}{
		{name: "unknown 403", status: 403, retry: RetryNextCandidate, effect: EffectNone},
		{name: "code only", status: 403, evidence: execution.ErrorEvidence{Code: "permission_error"}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "message only", status: 403, evidence: execution.ErrorEvidence{Summary: "permission_error"}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "type substring", status: 403, evidence: execution.ErrorEvidence{Type: "other_permission_error"}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "type case mismatch", status: 403, evidence: execution.ErrorEvidence{Type: "PERMISSION_ERROR"}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "type whitespace", status: 403, evidence: execution.ErrorEvidence{Type: " permission_error "}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "client origin", status: 403, evidence: execution.ErrorEvidence{Type: "permission_error", OriginHint: execution.ErrorOriginClient}, retry: RetryNone, effect: EffectNone},
		{name: "internal origin", status: 403, evidence: execution.ErrorEvidence{Type: "permission_error", OriginHint: execution.ErrorOriginInternal}, retry: RetryNone, effect: EffectNone},
		{name: "downstream origin", status: 403, evidence: execution.ErrorEvidence{Type: "permission_error", OriginHint: execution.ErrorOriginDownstream}, retry: RetryNone, effect: EffectNone},
		{name: "provider kind", status: 403, evidence: execution.ErrorEvidence{Type: "permission_error", Kind: execution.ErrorKindProvider}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "transport kind", status: 403, evidence: execution.ErrorEvidence{Type: "permission_error", Kind: execution.ErrorKindTransport}, retry: RetryNone, effect: EffectNone},
		{name: "401 unchanged", status: 401, evidence: execution.ErrorEvidence{Type: "permission_error"}, retry: RetryNextCandidate, effect: EffectRecordCredentialFailure},
		{name: "429 unchanged", status: 429, evidence: execution.ErrorEvidence{Type: "permission_error"}, retry: RetryNextCandidate, effect: EffectCooldownModel},
		{name: "500 unchanged", status: 500, evidence: execution.ErrorEvidence{Type: "permission_error"}, retry: RetryNextCandidate, effect: EffectSkipGroup},
		{name: "503 unchanged", status: 503, evidence: execution.ErrorEvidence{Type: "permission_error"}, retry: RetryNextCandidate, effect: EffectSkipGroup},
		{name: "response status overrides evidence", status: 400, evidence: execution.ErrorEvidence{Type: "permission_error", StatusCode: 403}, retry: RetryNextCandidate, effect: EffectNone},
		{name: "no status", evidence: execution.ErrorEvidence{Type: "permission_error"}, retry: RetryNone, effect: EffectNone},
		{name: "committed outer 200", status: 200, evidence: execution.ErrorEvidence{Type: "permission_error", StatusCode: 403}, committed: true, retry: RetryNone, effect: EffectNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := test.evidence
			if evidence.Kind == "" {
				evidence.Kind = execution.ErrorKindHTTP
			}
			got := JudgeExecution(ExecutionAttempt{
				DispatchState: execution.DispatchMaybeSent, StatusCode: test.status,
				Evidence: &evidence, DownstreamCommitted: test.committed, Now: time.Unix(100, 0),
			}, DecisionContext{Method: http.MethodPost, Operation: execution.OperationChatCompletion})
			if got.RuleID == "request.permission_denied" || got.Retry != test.retry || got.Effect != test.effect {
				t.Fatalf("decision=%+v, want existing retry=%s effect=%s", got, test.retry, test.effect)
			}
		})
	}
}
