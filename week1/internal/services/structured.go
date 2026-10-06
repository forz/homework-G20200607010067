package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// StructuredValidator is compiled once per gateway request and reused for a
// possible correction attempt and stream terminal validation.
type StructuredValidator struct {
	format *domain.JSONSchemaFormat
	schema *jsonschema.Schema
}

func compileStructured(format *domain.JSONSchemaFormat, maxBytes, maxDepth int) (*StructuredValidator, error) {
	if format == nil {
		return nil, nil
	}
	if len(format.Schema) == 0 || len(format.Schema) > maxBytes {
		return nil, core.NewError(
			http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json_schema", "json_schema is invalid or too large",
		).WithParam("response_format.json_schema.schema")
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(format.Schema))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil || schemaDepth(document, 1) > maxDepth {
		return nil, core.WrapError(
			http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json_schema", "json_schema is invalid or too deep", err,
		).WithParam("response_format.json_schema.schema")
	}
	compiler := jsonschema.NewCompiler()
	const resource = "urn:gateway:request-schema"
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, invalidSchema(err)
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return nil, invalidSchema(err)
	}
	return &StructuredValidator{format: format, schema: compiled}, nil
}

// ValidateText requires exactly one JSON value that conforms to the compiled schema.
func (v *StructuredValidator) ValidateText(content string) error {
	if v == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewBufferString(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalidStructuredOutput(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidStructuredOutput(err)
	}
	if err := v.schema.Validate(value); err != nil {
		return invalidStructuredOutput(err)
	}
	return nil
}

func schemaDepth(value any, depth int) int {
	maximum := depth
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			maximum = max(maximum, schemaDepth(child, depth+1))
		}
	case []any:
		for _, child := range typed {
			maximum = max(maximum, schemaDepth(child, depth+1))
		}
	}
	return maximum
}

func invalidSchema(cause error) *core.GatewayError {
	return core.WrapError(
		http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_json_schema", "json_schema could not be compiled", cause,
	).WithParam("response_format.json_schema.schema")
}

func invalidStructuredOutput(cause error) *core.GatewayError {
	return core.WrapError(
		http.StatusUnprocessableEntity, core.ErrorTypeUpstream, "invalid_structured_output",
		"model output did not match the requested JSON schema", cause,
	)
}
