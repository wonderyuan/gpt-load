package subscriptionruntime

import (
	"context"
	"testing"
)

func TestResetCreditExpiryConditionContext(t *testing.T) {
	t.Parallel()
	for _, ctx := range []context.Context{nil, context.Background()} {
		if condition, ok := ResetCreditExpiryFromContext(ctx); ok || condition != (ResetCreditExpiryCondition{}) {
			t.Fatalf("unconditioned context = %#v, %v", condition, ok)
		}
	}
	condition := ResetCreditExpiryCondition{ExpiresAtMS: 1_800_001_800_000, NowMS: 1_800_000_000_000}
	ctx := WithResetCreditExpiryCondition(nil, condition)
	condition.NowMS++
	got, ok := ResetCreditExpiryFromContext(ctx)
	if !ok || got.NowMS != 1_800_000_000_000 || got.ExpiresAtMS != condition.ExpiresAtMS {
		t.Fatalf("stored condition = %#v, %v", got, ok)
	}
	parent, cancel := context.WithCancel(ctx)
	child := WithResetCreditExpiryCondition(parent, ResetCreditExpiryCondition{})
	cancel()
	if got, ok := ResetCreditExpiryFromContext(child); !ok || got != (ResetCreditExpiryCondition{}) || child.Err() != context.Canceled {
		t.Fatalf("child condition/cancellation = %#v, %v, %v", got, ok, child.Err())
	}
	if got, _ := ResetCreditExpiryFromContext(ctx); got.NowMS != 1_800_000_000_000 {
		t.Fatal("child changed parent condition")
	}
}
