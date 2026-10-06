package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/homework-G20200607010067/week1/internal/domain"
	"gopkg.in/yaml.v3"
)

const (
	DefaultPath = "configs/gateway.yaml"
	ModelPro    = "deepseek-v4-pro"
	ModelFlash  = "deepseek-v4-flash"
)

// Duration decodes human-readable YAML duration strings.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", node.Value, err)
	}
	d.Duration = parsed
	return nil
}

// Config is the validated process configuration.
type Config struct {
	Server        ServerConfig           `yaml:"server"`
	Database      DatabaseConfig         `yaml:"database"`
	Retry         RetryConfig            `yaml:"retry"`
	Limits        LimitsConfig           `yaml:"limits"`
	Models        map[string]ModelConfig `yaml:"models"`
	GatewayAPIKey string                 `yaml:"-"`
}

type ServerConfig struct {
	Address     string   `yaml:"address"`
	ReadTimeout Duration `yaml:"read_timeout"`
	IdleTimeout Duration `yaml:"idle_timeout"`
}

type DatabaseConfig struct {
	Path        string   `yaml:"path"`
	BusyTimeout Duration `yaml:"busy_timeout"`
}

type RetryConfig struct {
	MaxRetries int      `yaml:"max_retries"`
	BaseDelay  Duration `yaml:"base_delay"`
	MaxDelay   Duration `yaml:"max_delay"`
}

type LimitsConfig struct {
	MaxBodyBytes         int64 `yaml:"max_body_bytes"`
	MaxMessages          int   `yaml:"max_messages"`
	MaxMessageBytes      int   `yaml:"max_message_bytes"`
	MaxTemplateBytes     int   `yaml:"max_template_bytes"`
	MaxRenderedBytes     int   `yaml:"max_rendered_prompt_bytes"`
	MaxSchemaBytes       int   `yaml:"max_schema_bytes"`
	MaxSchemaDepth       int   `yaml:"max_schema_depth"`
	MaxStreamBufferBytes int   `yaml:"max_stream_buffer_bytes"`
	MaxQueryLimit        int   `yaml:"max_query_limit"`
}

type ModelConfig struct {
	Protocol      domain.Protocol `yaml:"protocol"`
	UpstreamModel string          `yaml:"upstream_model"`
	BaseURL       string          `yaml:"base_url"`
	APIKey        string          `yaml:"-"`
	Timeout       Duration        `yaml:"timeout"`
	MaxTokens     int             `yaml:"max_tokens"`
	RatePerSecond float64         `yaml:"rate_per_second"`
	Burst         int             `yaml:"burst"`
}

// Load reads YAML, applies documented environment overrides, and rejects an
// unsafe or incomplete configuration before the server becomes ready.
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("GATEWAY_CONFIG")
	}
	if path == "" {
		path = DefaultPath
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read gateway config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(contents, &cfg); err != nil {
		return nil, fmt.Errorf("decode gateway config: %w", err)
	}
	if err := cfg.applyEnvironment(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate enforces fixed model aliases, protocol bindings, and bounded settings.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("gateway config is nil")
	}
	if strings.TrimSpace(c.Server.Address) == "" {
		return errors.New("server.address is required")
	}
	if c.Server.ReadTimeout.Duration <= 0 || c.Server.IdleTimeout.Duration <= 0 {
		return errors.New("server timeouts must be positive")
	}
	if strings.TrimSpace(c.Database.Path) == "" || c.Database.BusyTimeout.Duration <= 0 {
		return errors.New("database path and positive busy timeout are required")
	}
	if c.Retry.MaxRetries < 0 || c.Retry.MaxRetries > 3 {
		return errors.New("retry.max_retries must be between 0 and 3")
	}
	if c.Retry.BaseDelay.Duration < 0 || c.Retry.MaxDelay.Duration < c.Retry.BaseDelay.Duration {
		return errors.New("retry delays are invalid")
	}
	positiveLimits := []int64{
		c.Limits.MaxBodyBytes, int64(c.Limits.MaxMessages), int64(c.Limits.MaxMessageBytes),
		int64(c.Limits.MaxTemplateBytes), int64(c.Limits.MaxRenderedBytes),
		int64(c.Limits.MaxSchemaBytes), int64(c.Limits.MaxSchemaDepth),
		int64(c.Limits.MaxStreamBufferBytes), int64(c.Limits.MaxQueryLimit),
	}
	for _, limit := range positiveLimits {
		if limit <= 0 {
			return errors.New("all request and query limits must be positive")
		}
	}
	if len(c.Models) != 2 {
		return errors.New("exactly two public model aliases are required")
	}
	if err := validateModel(c.Models, ModelPro, domain.ProtocolOpenAIResponses); err != nil {
		return err
	}
	return validateModel(c.Models, ModelFlash, domain.ProtocolAnthropicMessages)
}

func validateModel(models map[string]ModelConfig, alias string, protocol domain.Protocol) error {
	model, ok := models[alias]
	if !ok {
		return fmt.Errorf("required model alias %q is missing", alias)
	}
	if model.Protocol != protocol {
		return fmt.Errorf("model %q must use protocol %q", alias, protocol)
	}
	if strings.TrimSpace(model.UpstreamModel) == "" || strings.TrimSpace(model.APIKey) == "" {
		return fmt.Errorf("model %q requires upstream_model and api_key", alias)
	}
	parsed, err := url.Parse(model.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("model %q has an invalid base_url", alias)
	}
	if model.Timeout.Duration <= 0 || model.MaxTokens <= 0 {
		return fmt.Errorf("model %q timeout and max_tokens must be positive", alias)
	}
	if math.IsNaN(model.RatePerSecond) || math.IsInf(model.RatePerSecond, 0) || model.RatePerSecond <= 0 || model.Burst < 1 {
		return fmt.Errorf("model %q rate_per_second and burst must be positive", alias)
	}
	return nil
}

func (c *Config) applyEnvironment() error {
	if c.Models == nil {
		c.Models = make(map[string]ModelConfig, 2)
	}
	setString(&c.Server.Address, "GATEWAY_ADDRESS")
	setString(&c.Database.Path, "GATEWAY_DATABASE_PATH")
	c.GatewayAPIKey = os.Getenv("GATEWAY_API_KEY")
	if err := setInt(&c.Retry.MaxRetries, "GATEWAY_MAX_RETRIES"); err != nil {
		return err
	}
	deepseekAPIKey, err := requiredAPIKey("DEEPSEEK_API_KEY")
	if err != nil {
		return err
	}
	pro := c.Models[ModelPro]
	setString(&pro.BaseURL, "OPENAI_BASE_URL")
	pro.APIKey = deepseekAPIKey
	setString(&pro.UpstreamModel, "OPENAI_RESPONSES_MODEL_ID")
	if err := setDuration(&pro.Timeout, "OPENAI_RESPONSES_TIMEOUT"); err != nil {
		return err
	}
	if err := setFloat(&pro.RatePerSecond, "DEEPSEEK_V4_PRO_RATE_PER_SECOND"); err != nil {
		return err
	}
	if err := setInt(&pro.Burst, "DEEPSEEK_V4_PRO_BURST"); err != nil {
		return err
	}
	c.Models[ModelPro] = pro

	flash := c.Models[ModelFlash]
	setString(&flash.BaseURL, "ANTHROPIC_BASE_URL")
	flash.APIKey = deepseekAPIKey
	setString(&flash.UpstreamModel, "ANTHROPIC_MESSAGES_MODEL_ID")
	if err := setDuration(&flash.Timeout, "ANTHROPIC_MESSAGES_TIMEOUT"); err != nil {
		return err
	}
	if err := setFloat(&flash.RatePerSecond, "DEEPSEEK_V4_FLASH_RATE_PER_SECOND"); err != nil {
		return err
	}
	if err := setInt(&flash.Burst, "DEEPSEEK_V4_FLASH_BURST"); err != nil {
		return err
	}
	c.Models[ModelFlash] = flash
	return nil
}

func requiredAPIKey(name string) (string, error) {
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("environment variable %s is required", name)
	}
	return value, nil
}

func setString(target *string, name string) {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		*target = value
	}
}

func setInt(target *int, name string) error {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("environment %s must be an integer", name)
	}
	*target = parsed
	return nil
}

func setFloat(target *float64, name string) error {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("environment %s must be numeric", name)
	}
	*target = parsed
	return nil
}

func setDuration(target *Duration, name string) error {
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("environment %s must be a duration", name)
	}
	target.Duration = parsed
	return nil
}
