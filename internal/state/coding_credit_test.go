package state

import (
	"encoding/json"
	"strings"
	"testing"

	"gpt-load/internal/channel"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/pricing"
)

func TestGLMCodingCreditOptInValidation(t *testing.T) {
	base := func() CompileInput {
		return CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{
			ID: 5, Name: "coding", ChannelID: channel.ZhipuAI, ConnectionType: "api_key", Enabled: true,
			Params:   json.RawMessage(`{"base_url":"https://open.bigmodel.cn/api/coding/paas/v4"}`),
			Models:   []ModelConfig{{ID: "glm-5.3"}, {ID: "glm-5.3-flash", Alias: "flash"}},
			Settings: config.Settings{SettingGLMCodingCreditEnabled: true},
		}}}
	}
	input := base()
	snapshot, err := Compile(input)
	if err != nil || !snapshot.Groups[5].GLMCodingCreditEnabled {
		t.Fatalf("compile = %#v, %v", snapshot, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*CompileInput)
	}{
		{"PAYG", func(i *CompileInput) { i.Groups[0].Params = json.RawMessage(`{}`) }},
		{"custom channel", func(i *CompileInput) { i.Groups[0].ChannelID = channel.OpenAICompatible }},
		{"proxy", func(i *CompileInput) {
			i.Groups[0].Params = json.RawMessage(`{"base_url":"https://proxy.example/api/coding/paas/v4"}`)
		}},
		{"FlashX", func(i *CompileInput) {
			i.Groups[0].Models = append(i.Groups[0].Models, ModelConfig{ID: "glm-5.3-flashx"})
		}},
		{"group multiplier", func(i *CompileInput) { v := pricing.PriceMultiplier(500_000); i.Groups[0].PriceMultiplier = &v }},
		{"key multiplier", func(i *CompileInput) {
			v := pricing.PriceMultiplier(500_000)
			i.AccessKeys = []AccessKeyConfig{{ID: 1, Name: "client", KeyHash: "abc", Status: AccessKeyStatusActive, PriceMultiplier: &v}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := base()
			test.mutate(&in)
			if _, err := Compile(in); err == nil {
				t.Fatal("accepted incompatible credit configuration")
			}
		})
	}
	// Opt-out does not change existing API model names, prices or multipliers.
	input = base()
	input.Groups[0].Settings = nil
	input.Groups[0].Params = json.RawMessage(`{}`)
	input.Groups[0].Models = []ModelConfig{{ID: "glm-5.3-flashx"}}
	if snapshot, err = Compile(input); err != nil || snapshot.Groups[5].GLMCodingCreditEnabled {
		t.Fatalf("ordinary API changed: %#v, %v", snapshot, err)
	}
}

func TestGLMCodingCreditSettingIsStrictAndGroupOnly(t *testing.T) {
	for _, value := range []any{nil, "true", 1, json.Number("1"), map[string]any{}} {
		if _, err := ResolveGroupRuntimeSettings(DefaultRuntimeSettings(), config.Settings{SettingGLMCodingCreditEnabled: value}); err == nil {
			t.Fatalf("accepted %#v", value)
		}
	}
	for _, value := range []bool{false, true} {
		resolved, err := ResolveGroupRuntimeSettings(DefaultRuntimeSettings(), config.Settings{SettingGLMCodingCreditEnabled: value})
		if err != nil || resolved.GLMCodingCreditEnabled != value {
			t.Fatalf("resolve = %#v, %v", resolved, err)
		}
	}
	if _, err := ResolveRuntimeSettings(config.Settings{SettingGLMCodingCreditEnabled: true}); err == nil || !strings.Contains(err.Error(), "unknown runtime setting") {
		t.Fatalf("unexpected global setting result: %v", err)
	}
}
