package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

const Latest = "latest"

const maxSkillEntries = 100

var Tools = []string{"list_files", "glob_files", "search_files", "read_file", "write_file", "edit_file", "move_file", "delete_file", "list_skill_resources", "read_skill_resource", "read_attachment", run.PythonTool, run.JavaScriptTool, run.GoTool, run.CommandTool, "update_plan", "publish_artifact"}

func ValidName(s string) bool { return namePattern.MatchString(s) }
func ValidLabel(s string) bool {
	return versionPattern.MatchString(s) && !strings.EqualFold(s, "latest")
}
func ValidateDocument(kind, name string, doc Document, publish bool) (*Config, error) {
	if !ValidName(name) || (kind != "agent" && kind != "skill") || !utf8.ValidString(doc.Markdown) || !utf8.ValidString(doc.ConfigYAML) || len(doc.Markdown) > MaxDocument || len(doc.ConfigYAML) > 64<<10 || len(doc.Assets) > maxSkillEntries {
		return nil, ErrConfig
	}
	if kind == "agent" && doc.Assets != nil {
		return nil, ErrConfig
	}
	seen := map[string]bool{}
	for _, a := range doc.Assets {
		key := strings.ToLower(a.Path)
		if !resource.ValidSkillPath(a.Path) || seen[key] || !storage.ValidID(a.ObjectID) || !hashPattern.MatchString(a.SHA256) || a.Size < 0 || a.Size > MaxResource {
			return nil, ErrConfig
		}
		seen[key] = true
	}
	// Only user-created directories are stored. Their ancestors are implied.
	if len(doc.Directories) > maxSkillEntries || kind != "skill" && len(doc.Directories) > 0 {
		return nil, ErrConfig
	}
	explicit := map[string]bool{}
	folders := map[string]string{}
	for _, dir := range doc.Directories {
		key := strings.ToLower(dir)
		if !resource.ValidSkillPath(dir) || explicit[key] {
			return nil, ErrConfig
		}
		explicit[key] = true
		for p := dir; p != "."; p = path.Dir(p) {
			key := strings.ToLower(p)
			if name := folders[key]; seen[key] || name != "" && name != p {
				return nil, ErrConfig
			}
			folders[key] = p
		}
	}
	for _, file := range doc.Assets {
		for p := path.Dir(file.Path); p != "."; p = path.Dir(p) {
			key := strings.ToLower(p)
			if name := folders[key]; seen[key] || name != "" && name != p {
				return nil, ErrConfig
			}
		}
	}
	if publish && strings.TrimSpace(doc.Markdown) == "" {
		return nil, ErrConfig
	}
	if kind == "skill" {
		if doc.ConfigYAML != "" {
			return nil, ErrConfig
		}
		return nil, nil
	}
	if !publish && strings.TrimSpace(doc.ConfigYAML) == "" {
		return nil, nil
	}
	var c Config
	if strictJSON(doc.ConfigYAML, &c) != nil || c.SchemaVersion != 1 || c.Name != name || !storage.ValidID(c.Model) || len(c.Skills) > 32 || len(c.Tools) > len(Tools) {
		return nil, ErrConfig
	}
	c.Context.Normalize()
	return validateConfig(c, !publish)
}

func strictJSON(raw string, value any) error {
	if !utf8.ValidString(raw) {
		return ErrConfig
	}
	check := json.NewDecoder(strings.NewReader(raw))
	check.UseNumber()
	first, err := check.Token()
	if err != nil || first != json.Delim('{') || !uniqueJSONKeys(check, '{', 1) {
		return ErrConfig
	}
	if _, err := check.Token(); err != io.EOF {
		return ErrConfig
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(value)
}

func uniqueJSONKeys(dec *json.Decoder, open json.Delim, depth int) bool {
	if depth > 12 {
		return false
	}
	keys := map[string]bool{}
	for dec.More() {
		if open == '{' {
			token, err := dec.Token()
			key, ok := token.(string)
			// Agent and project schema keys are lowercase ASCII.
			if err != nil || !ok || keys[key] || strings.IndexFunc(key, func(r rune) bool {
				return r >= utf8.RuneSelf || 'A' <= r && r <= 'Z'
			}) >= 0 {
				return false
			}
			keys[key] = true
		}
		token, err := dec.Token()
		if err != nil {
			return false
		}
		if nested, ok := token.(json.Delim); ok && !uniqueJSONKeys(dec, nested, depth+1) {
			return false
		}
	}
	_, err := dec.Token()
	return err == nil
}

func validateConfig(c Config, allowLatest bool) (*Config, error) {
	ids := map[string]bool{}
	for _, r := range c.Skills {
		if !storage.ValidID(r.ID) || (!ValidLabel(r.Version) && (!allowLatest || r.Version != Latest)) || ids[r.ID] {
			return nil, ErrConfig
		}
		ids[r.ID] = true
	}
	allowed := map[string]bool{}
	for _, t := range Tools {
		allowed[t] = true
	}
	used := map[string]bool{}
	for _, t := range c.Tools {
		if !allowed[t] || used[t] {
			return nil, ErrConfig
		}
		used[t] = true
	}
	if c.Context.MaxInputTokens < 1 || c.Context.MaxInputTokens > 2000000 || c.Context.MaxTokens < 1 || c.Context.MaxTokens > 1000000 || (c.Context.Compression != "summary" && c.Context.Compression != "reject") || c.Limits.MaxSteps < 1 || c.Limits.MaxSteps > 1000 || c.Limits.TimeoutSeconds < 1 || c.Limits.TimeoutSeconds > 86400 {
		return nil, ErrConfig
	}
	return &c, nil
}
func Equal(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }
