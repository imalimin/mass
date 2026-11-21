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
	"go.yaml.in/yaml/v4"
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
	if strictYAML(doc.ConfigYAML, &c) != nil || c.SchemaVersion != 1 || c.Name != name || !storage.ValidID(c.Model) || len(c.Skills) > 32 || len(c.Tools) > len(Tools) {
		return nil, ErrConfig
	}
	c.Context.Normalize()
	return validateConfig(c, !publish)
}

// NormalizeAgentConfig converts validated legacy text while preserving JSON edits.
func (doc *Document) NormalizeAgentConfig(config *Config) {
	if config != nil && !json.Valid([]byte(doc.ConfigYAML)) {
		raw, _ := json.MarshalIndent(config, "", "  ")
		doc.ConfigYAML = string(raw)
	}
}

func strictYAML(raw string, value any) error {
	var tree yaml.Node
	dec := yaml.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&tree); err != nil {
		return ErrConfig
	}
	if len(tree.Content) != 1 || tree.Content[0].Kind != yaml.MappingNode {
		return ErrConfig
	}
	var walk func(*yaml.Node, int) bool
	walk = func(n *yaml.Node, depth int) bool {
		if depth > 12 || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Tag != "!!str" || k.Value == "<<" || keys[k.Value] {
					return false
				}
				keys[k.Value] = true
			}
		}
		for _, c := range n.Content {
			if !walk(c, depth+1) {
				return false
			}
		}
		return true
	}
	if !walk(&tree, 0) {
		return ErrConfig
	}
	var extra yaml.Node
	if dec.Decode(&extra) != io.EOF {
		return ErrConfig
	}
	dec = yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	return dec.Decode(value)
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
