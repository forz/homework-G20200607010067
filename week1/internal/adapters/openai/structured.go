package openai

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/homework-G20200607010067/week1/internal/domain"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

func (a *Adapter) modelFor(ctx context.Context, request *domain.ModelRequest) (*agenticopenai.ResponsesModel, error) {
	if request.ResponseFormat == nil {
		return a.model, nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(request.ResponseFormat.Schema, &schemaMap); err != nil {
		return nil, err
	}
	format := responses.ResponseFormatTextConfigParamOfJSONSchema(request.ResponseFormat.Name, schemaMap)
	format.OfJSONSchema.Strict = param.NewOpt(request.ResponseFormat.Strict)
	requestConfig := a.baseConfig
	requestConfig.Text = &responses.ResponseTextConfigParam{Format: format}
	return agenticopenai.NewResponsesModel(ctx, &requestConfig)
}
