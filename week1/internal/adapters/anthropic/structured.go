package anthropic

import (
	"encoding/json"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func applyStructured(params *sdk.MessageNewParams, format *domain.JSONSchemaFormat) error {
	if format == nil {
		return nil
	}
	var schemaMap map[string]any
	if err := json.Unmarshal(format.Schema, &schemaMap); err != nil {
		return err
	}
	params.OutputConfig.Format = sdk.JSONOutputFormatParam{Schema: schemaMap}
	// Some Messages-compatible providers ignore output_config.format. Include
	// the schema in the instruction too; the service still validates every result.
	params.System = append(params.System, sdk.TextBlockParam{
		Text: "Return only one JSON value matching this JSON schema, without Markdown: " + string(format.Schema),
	})
	return nil
}
