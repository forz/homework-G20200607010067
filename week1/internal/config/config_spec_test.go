package config_test

import (
	"math"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestValidateRejectsNonFiniteRates(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		configured := validConfig()
		model := configured.Models[config.ModelPro]
		model.RatePerSecond = value
		configured.Models[config.ModelPro] = model
		if err := configured.Validate(); err == nil {
			t.Fatalf("non-finite model rate %v was accepted", value)
		}
	}
}

func TestValidateRequiresFixedAliasProtocolBindings_BitsSpecUT(t *testing.T) {
	valid := validConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid fixed model configuration was rejected: %v", err)
	}

	invalid := validConfig()
	pro := invalid.Models[config.ModelPro]
	pro.Protocol = domain.ProtocolAnthropicMessages
	invalid.Models[config.ModelPro] = pro
	if err := invalid.Validate(); err == nil {
		t.Fatal("mismatched public alias and upstream protocol was accepted")
	}
}

func TestValidateRejectsRetryBudgetAboveThree_BitsSpecUT(t *testing.T) {
	configured := validConfig()
	configured.Retry.MaxRetries = 4
	if err := configured.Validate(); err == nil {
		t.Fatal("retry budget above three was accepted")
	}
}

func validConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Address: "127.0.0.1:8080", ReadTimeout: duration(time.Second), IdleTimeout: duration(time.Second),
		},
		Database: config.DatabaseConfig{Path: "data/test.db", BusyTimeout: duration(time.Second)},
		Retry:    config.RetryConfig{MaxRetries: 3, BaseDelay: duration(time.Millisecond), MaxDelay: duration(time.Second)},
		Limits: config.LimitsConfig{
			MaxBodyBytes: 1, MaxMessages: 1, MaxMessageBytes: 1, MaxTemplateBytes: 1,
			MaxRenderedBytes: 1, MaxSchemaBytes: 1, MaxSchemaDepth: 1,
			MaxStreamBufferBytes: 1, MaxQueryLimit: 1,
		},
		Models: map[string]config.ModelConfig{
			config.ModelPro:   model(domain.ProtocolOpenAIResponses),
			config.ModelFlash: model(domain.ProtocolAnthropicMessages),
		},
	}
}

func duration(value time.Duration) config.Duration { return config.Duration{Duration: value} }

func model(protocol domain.Protocol) config.ModelConfig {
	return config.ModelConfig{
		Protocol: protocol, UpstreamModel: "upstream", BaseURL: "https://example.com",
		APIKey: "test", Timeout: duration(time.Second), MaxTokens: 1, RatePerSecond: 1, Burst: 1,
	}
}
