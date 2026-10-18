package integration

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v4"
)

func contractValidator(t *testing.T) func(string, string, *httptest.ResponseRecorder) {
	t.Helper()
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("https://mass.test/openapi", document); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for name := range document["components"].(map[string]any)["schemas"].(map[string]any) {
		schema, e := compiler.Compile("https://mass.test/openapi#/components/schemas/" + name)
		if e != nil {
			t.Fatal(e)
		}
		schemas[name] = schema
	}
	return func(method, path string, w *httptest.ResponseRecorder) {
		t.Helper()
		name := "Error"
		if w.Code < 400 {
			path = "/api/v1" + strings.Split(path, "?")[0]
			var selected map[string]any
			for pattern, value := range document["paths"].(map[string]any) {
				matcher := "^" + regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(pattern, `[^/]+`) + "$"
				if regexp.MustCompile(matcher).MatchString(path) {
					selected = value.(map[string]any)
					break
				}
			}
			if selected == nil {
				t.Fatalf("path absent in OpenAPI: %s", path)
			}
			operation, ok := selected[strings.ToLower(method)].(map[string]any)
			if !ok {
				t.Fatalf("method absent in OpenAPI: %s %s", method, path)
			}
			if w.Code == 204 {
				if w.Body.Len() != 0 {
					t.Fatal("204 has a body")
				}
				return
			}
			responses := operation["responses"].(map[string]any)
			var response map[string]any
			if w.Code == 201 {
				response = responses["201"].(map[string]any)
			} else {
				response = responses["200"].(map[string]any)
			}
			ref := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"].(string)
			name = strings.TrimPrefix(ref, "#/components/schemas/")
		}
		var value any
		if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if err := schemas[name].Validate(value); err != nil {
			t.Fatalf("response violates %s: %v", name, err)
		}
	}
}
