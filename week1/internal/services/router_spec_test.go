package services_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/homework-G20200607010067/week1/internal/config"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
	"github.com/homework-G20200607010067/week1/internal/services"
)

func TestModelRouterResolvesOnlyConfiguredAliasesAndListsStably_BitsSpecUT(t *testing.T) {
	models := map[string]config.ModelConfig{
		config.ModelFlash: {Protocol: domain.ProtocolAnthropicMessages, UpstreamModel: "flash-upstream"},
		config.ModelPro:   {Protocol: domain.ProtocolOpenAIResponses, UpstreamModel: "pro-upstream"},
	}
	router, err := services.NewModelRouter(models, map[string]domain.ModelAdapter{
		config.ModelFlash: stubAdapter{protocol: domain.ProtocolAnthropicMessages},
		config.ModelPro:   stubAdapter{protocol: domain.ProtocolOpenAIResponses},
	})
	if err != nil {
		t.Fatalf("construct router: %v", err)
	}
	wantModels := []string{config.ModelFlash, config.ModelPro}
	if got := router.Models(); !reflect.DeepEqual(got, wantModels) {
		t.Fatalf("models = %v, want %v", got, wantModels)
	}
	route, err := router.Resolve(config.ModelPro)
	if err != nil || route.UpstreamModel != "pro-upstream" || route.Adapter.Protocol() != domain.ProtocolOpenAIResponses {
		t.Fatalf("resolved route = %#v, err=%v", route, err)
	}
	_, err = router.Resolve("unknown")
	if err == nil || core.NormalizeError(err).Code != "model_not_found" {
		t.Fatalf("unknown model error = %v, want model_not_found", err)
	}
}

type stubAdapter struct{ protocol domain.Protocol }

func (a stubAdapter) Protocol() domain.Protocol { return a.protocol }
func (stubAdapter) Generate(context.Context, *domain.ModelRequest) (*domain.ModelResponse, error) {
	return nil, nil
}
func (stubAdapter) Stream(context.Context, *domain.ModelRequest) (domain.ModelStream, error) {
	return nil, nil
}
