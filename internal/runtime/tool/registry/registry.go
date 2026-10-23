// Package registry resolves a fixed, versioned platform tool subset.
package registry

import (
	"encoding/json"

	"github.com/imalimin/mass/internal/domain/run"
)

type Registry struct{}

func Specs() []run.ToolSpec {
	return []run.ToolSpec{
		{Name: "read_file", Version: "1", Description: "Read one UTF-8 file in the current session workspace, returning its complete content and SHA-256. Limited to 1 MiB.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}}}`)},
		{Name: "write_file", Version: "1", Description: "Create or replace a UTF-8 file in the current session workspace. expected_sha256 must be 'missing' for a new file, or the current file digest when updating. Returns the new SHA-256.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","content","media_type","expected_sha256"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"content":{"type":"string","maxLength":1048576},"media_type":{"type":"string","maxLength":128},"expected_sha256":{"type":"string","pattern":"^(missing|[a-f0-9]{64})$"}}}`)},
		{Name: "publish_artifact", Version: "1", Description: "Save an immutable copy of an existing workspace file and provide an authenticated download URL. expected_sha256 must equal the current file digest.", Schema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["path","expected_sha256","display_name","media_type"],"properties":{"path":{"type":"string","minLength":1,"maxLength":1024},"expected_sha256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"display_name":{"type":"string","minLength":1,"maxLength":200},"media_type":{"type":"string","maxLength":128}}}`)},
	}
}
func (r *Registry) Describe(selected []run.ToolSpec) ([]run.ToolSpec, error) {
	result := []run.ToolSpec{}
	seen := map[string]bool{}
	for _, wanted := range selected {
		found := false
		for _, s := range Specs() {
			if s.Name == wanted.Name && s.Version == wanted.Version {
				if seen[s.Name] {
					return nil, run.ErrTool
				}
				seen[s.Name] = true
				result = append(result, s)
				found = true
				break
			}
		}
		if !found {
			return nil, run.ErrTool
		}
	}
	return result, nil
}
