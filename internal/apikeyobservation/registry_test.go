package apikeyobservation

import (
	"encoding/json"
	"testing"

	"gpt-load/internal/channel"
	subscriptionproviders "gpt-load/internal/subscription/providers"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func TestRegistryFailsClosed(t *testing.T) {
	t.Parallel()
	channels := channel.NewRegistry()
	if _, err := NewRegistry(nil, ZhipuObserver{}); err == nil {
		t.Fatal("nil channels accepted")
	}
	if _, err := NewRegistry(channels); err == nil {
		t.Fatal("missing bound observer accepted")
	}
	if _, err := NewRegistry(channels, nil); err == nil {
		t.Fatal("nil observer accepted")
	}
	if _, err := NewRegistry(channels, ZhipuObserver{}, ZhipuObserver{}); err == nil {
		t.Fatal("duplicate observer accepted")
	}
}

func TestRegistryOnlySupportsOfficialCNCodingTarget(t *testing.T) {
	t.Parallel()
	channels := channel.NewRegistry()
	registry, err := NewRegistry(channels, ZhipuObserver{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		base string
		want bool
	}{
		{"https://open.bigmodel.cn/api/coding/paas/v4", true},
		{"https://open.bigmodel.cn:443/api/coding/paas/v4/", true},
		{"https://open.bigmodel.cn/api/paas/v4", false},
		{"https://api.z.ai/api/coding/paas/v4", false},
		{"https://proxy.example/api/coding/paas/v4", false},
		{"https://open.bigmodel.cn.evil.example/api/coding/paas/v4", false},
		{"http://open.bigmodel.cn/api/coding/paas/v4", false},
		{"https://open.bigmodel.cn:444/api/coding/paas/v4", false},
		{"https://open.bigmodel.cn/api/coding/paas/v4?foo=bar", false},
		{"https://open.bigmodel.cn/api/coding/paas/v4#fragment", false},
		{"https://user@open.bigmodel.cn/api/coding/paas/v4", false},
	} {
		config, _ := json.Marshal(map[string]string{"base_url": test.base})
		target := channel.ResolvedTarget{ChannelID: channel.ZhipuAI, TargetConfig: config}
		if _, ok := registry.Resolve(target); ok != test.want {
			t.Fatalf("target %s supported=%v", test.base, ok)
		}
	}
	defaultTarget, err := channels.Resolve(channel.ZhipuAI, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Resolve(defaultTarget); ok {
		t.Fatal("default PAYG target opted in")
	}
	target := zhipuRequest(t).Target
	target.ChannelID = channel.OpenAI
	if _, ok := registry.Resolve(target); ok {
		t.Fatal("other channel opted in")
	}
}

func TestAPIKeyQuotaNeverAcquiresSubscriptionLifecycle(t *testing.T) {
	t.Parallel()
	runtime, err := subscriptionruntime.NewRuntime(channel.NewRegistry(), subscriptionproviders.Implementations()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Driver(channel.ZhipuAI); ok {
		t.Fatal("API-key quota acquired subscription driver")
	}
	if _, ok := runtime.QuotaObservation(channel.ZhipuAI); ok {
		t.Fatal("API-key observer registered in subscription runtime")
	}
	if _, ok := runtime.ResetCreditAction(channel.ZhipuAI); ok {
		t.Fatal("API-key acquired reset action")
	}
	for _, id := range runtime.ChannelIDs() {
		if id == channel.ZhipuAI {
			t.Fatal("API-key included in subscription channel IDs")
		}
	}
}
