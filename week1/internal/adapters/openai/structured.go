package openai

import (
	"encoding/json"

	"github.com/homework-G20200607010067/week1/internal/domain"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

func responseTextConfig(responseFormat *domain.JSONSchemaFormat) (*responses.ResponseTextConfigParam, error) {
	if responseFormat == nil {
		return nil, nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(responseFormat.Schema, &schemaMap); err != nil {
		return nil, err
	}
	format := responses.ResponseFormatTextConfigParamOfJSONSchema(responseFormat.Name, schemaMap)
	format.OfJSONSchema.Strict = param.NewOpt(responseFormat.Strict)
	return &responses.ResponseTextConfigParam{Format: format}, nil
}
