package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

type PublishEntry struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	ID        string `json:"id,omitempty"`
	VersionID string `json:"version_id,omitempty"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status,omitempty"`
	revision  int64
	committed bool
}

type PublishManifest struct {
	Agent       PublishEntry              `json:"agent"`
	Skills      []PublishEntry            `json:"skills"`
	ModelID     string                    `json:"model_id"`
	ModelLocked bool                      `json:"model_locked,omitempty"`
	Publication run.CreativityPublication `json:"-"`
}

type projectSkillDraft struct {
	entry         PublishEntry
	path          string
	base          Version
	baseRevision  int64
	sourceVersion string
	document      Document
	files         map[string][]byte
}

type PublishProject struct {
	agent    PublishEntry
	config   Config
	markdown string
	skills   []projectSkillDraft
}

type projectCatalog interface {
	formalVersions
	CatalogSaveBasedOn(context.Context, string, string, Draft, string) error
	CatalogPublishBasedOn(context.Context, string, string, *Version, string) error
}

const (
	publishNew       = "create"
	publishUpdate    = "update"
	publishReference = "reference"
)

var ErrPublishModelRequired = errors.New("publish_model_required")

func projectError(name, reason string) error {
	return &resource.ProjectFileError{Path: name, Reason: reason}
}

func (p PublishProject) Manifest() PublishManifest {
	manifest := PublishManifest{Agent: p.agent, Skills: make([]PublishEntry, 0, len(p.skills)), ModelID: p.config.Model}
	for _, skill := range p.skills {
		manifest.Skills = append(manifest.Skills, skill.entry)
	}
	return manifest
}

func (p PublishProject) PendingResult() PublishManifest {
	result := p.Manifest()
	result.Agent.Status = "unprocessed"
	for index := range result.Skills {
		entry := &result.Skills[index]
		entry.Status = "unprocessed"
		if entry.Action == publishReference {
			entry.Status = "referenced"
		}
	}
	return result
}

func (p PublishProject) SupportsModel(model identity.Model) bool {
	return p.config.SupportsModel(model)
}

func (s *Service) PreparePublishAgent(ctx context.Context, actor, space string, session run.Creativity, files resource.ProjectFiles, modelID string) (PublishProject, error) {
	project := PublishProject{}
	required := func(name string) ([]byte, error) {
		if slices.Contains(files.Oversized, name) {
			return nil, projectError(name, "超出发布文件大小限制")
		}
		if slices.Contains(files.LocalFiles, name) {
			return nil, projectError(name, "文件只保存在宿主，未进入 Run 快照")
		}
		raw, found := files.Files[name]
		if !found {
			return nil, projectError(name, "快照内缺少文件")
		}
		return raw, nil
	}
	const configPath = resource.AgentConfigPath
	config, err := required(configPath)
	if err != nil {
		return project, err
	}
	if len(config) > 64<<10 || strictJSON(string(config), &project.config) != nil {
		return project, projectError(configPath, "Agent 配置无效")
	}
	if project.config.Model == "" {
		if modelID == "" {
			return project, ErrPublishModelRequired
		}
		project.config.Model = modelID
	} else if modelID != "" {
		return project, projectError(configPath, "Agent 已绑定模型，请返回创意会话更改")
	}
	markdown, err := required("AGENTS.md")
	if err != nil {
		return project, err
	}
	project.config.Skills = []SkillRef{}
	project.markdown = string(markdown)
	project.agent = PublishEntry{Name: project.config.Name, ID: session.TargetAgentID, Action: publishNew}
	if session.TargetAgentID != "" {
		draft, err := s.Repo.CatalogDraft(ctx, actor, space, "agent", session.TargetAgentID)
		if err != nil {
			return project, err
		}
		if draft.Archived {
			return project, projectError(configPath, "目标 Agent 已归档，请先恢复")
		}
		project.agent.Name, project.agent.Action = draft.Name, publishUpdate
	}
	if _, err := ValidateDocument("agent", project.agent.Name, project.agentDocument(nil), true); err != nil {
		return project, projectError("AGENTS.md / "+configPath, "正文或 Agent 配置无效")
	}
	return project, nil
}

func (s *Service) ScanPublishSkills(ctx context.Context, actor, space string, files resource.ProjectFiles, project PublishProject, published []run.PublishedSkill) (PublishProject, error) {
	const bindingsPath = resource.ProjectBindingsPath
	if len(files.Oversized) > 0 {
		return project, projectError(files.Oversized[0], "超出发布文件大小限制")
	}
	for _, local := range files.LocalFiles {
		parent := path.Dir(local)
		if local == bindingsPath || parent == "skills" || path.Base(local) == "SKILL.md" && skillFolder(parent) {
			return project, projectError(local, "文件只保存在宿主，未进入 Run 快照")
		}
	}
	if slices.Contains(files.Directories, bindingsPath) {
		return project, projectError(bindingsPath, "必须是文件")
	}
	var bindings []projectSkill
	if raw, found := files.Files[bindingsPath]; found {
		var err error
		bindings, err = parseProjectSkills(raw)
		if err != nil {
			return project, err
		}
	}
	byPath := make(map[string]run.PublishedSkill, len(published))
	byID := make(map[string]run.PublishedSkill, len(published))
	for _, skill := range published {
		byPath[skill.Path] = skill
		byID[skill.ID] = skill
	}
	load := func(ref projectSkill, sourceVersion string, revision int64) error {
		base, err := s.Version(ctx, actor, space, "skill", ref.SkillID, ref.VersionID)
		if errors.Is(err, identity.ErrNotFound) || err == nil && (base.Archived || base.Debug) {
			return projectError(bindingsPath, "Skill 版本不可用："+ref.SkillID)
		}
		if err != nil {
			return err
		}
		draft, err := s.Repo.CatalogDraft(ctx, actor, space, "skill", ref.SkillID)
		if err != nil {
			return err
		}
		if draft.Archived {
			return projectError(bindingsPath, "Skill 已归档："+draft.Name)
		}
		skill := projectSkillDraft{entry: PublishEntry{Name: draft.Name, Action: publishReference, ID: ref.SkillID, VersionID: base.ID, Label: base.Label},
			base: base, baseRevision: revision, sourceVersion: sourceVersion}
		if ref.Path != "" {
			skill.path = ref.Path
			skill.document, skill.files, err = readProjectSkill(files, ref.Path, draft.Name)
			if err != nil {
				return err
			}
			if !sameSkill(skill.document, base.Bundle.Document) {
				skill.entry.Action, skill.entry.VersionID, skill.entry.Label = publishUpdate, "", ""
			}
		}
		project.skills = append(project.skills, skill)
		return nil
	}
	usedPath, usedID := map[string]bool{}, map[string]bool{}
	for _, ref := range bindings {
		usedID[ref.SkillID] = true
		sourceVersion := ref.VersionID
		var revision int64
		if ref.Path != "" {
			usedPath[ref.Path] = true
			// Follow this session's publication when the local binding is still the same.
			prior := byID[ref.SkillID]
			if prior.SourceVersion == sourceVersion || prior.Version == sourceVersion {
				ref.VersionID, revision = prior.Version, prior.Revision
			}
		}
		if err := load(ref, sourceVersion, revision); err != nil {
			return project, err
		}
	}
	folders := []string{}
	for name := range files.Files {
		folder := path.Dir(name)
		if sub, found := strings.CutPrefix(folder, "skills/"); found && !strings.Contains(sub, "/") && path.Base(name) == "SKILL.md" && !skillFolder(folder) {
			return project, projectError(folder, "Skill 目录名无效")
		}
		if path.Base(name) == "SKILL.md" && skillFolder(folder) && !usedPath[folder] {
			folders = append(folders, folder)
		}
	}
	slices.Sort(folders)
	for _, folder := range folders {
		if prior := byPath[folder]; prior.ID != "" && prior.SourceVersion == "" && !usedID[prior.ID] {
			ref := projectSkill{SkillBase: run.SkillBase{SkillID: prior.ID, VersionID: prior.Version}, Path: folder}
			if err := load(ref, "", prior.Revision); err != nil {
				return project, err
			}
			usedID[prior.ID] = true
			continue
		}
		name := path.Base(folder)
		document, resources, err := readProjectSkill(files, folder, name)
		if err != nil {
			return project, err
		}
		project.skills = append(project.skills, projectSkillDraft{entry: PublishEntry{Name: name, Action: publishNew}, path: folder, document: document, files: resources})
	}
	if len(project.skills) > 32 {
		return project, projectError(bindingsPath, "Agent 最多可绑定 32 个 Skill")
	}
	return project, nil
}

func parseProjectSkills(raw []byte) ([]projectSkill, error) {
	const name = resource.ProjectBindingsPath
	var manifest struct {
		Skills []struct {
			run.SkillBase
			Path json.RawMessage `json:"path"`
		} `json:"skills"`
	}
	if len(raw) > 64<<10 || strictJSON(string(raw), &manifest) != nil {
		return nil, projectError(name, "Skill 绑定格式无效")
	}
	bindings := make([]projectSkill, 0, len(manifest.Skills))
	usedID, usedPath := map[string]bool{}, map[string]bool{}
	for index, ref := range manifest.Skills {
		if !storage.ValidID(ref.SkillID) || !storage.ValidID(ref.VersionID) || usedID[ref.SkillID] {
			return nil, projectError(name, "Skill ID、版本 ID 无效或绑定重复")
		}
		usedID[ref.SkillID] = true
		binding := projectSkill{SkillBase: ref.SkillBase}
		if ref.Path != nil {
			if json.Unmarshal(ref.Path, &binding.Path) != nil || !skillFolder(binding.Path) || usedPath[binding.Path] {
				return nil, projectError(name, fmt.Sprintf("skills[%d].path 无效或重复（Skill %s）；仅引用时请省略 path", index, ref.SkillID))
			}
			usedPath[binding.Path] = true
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

func skillFolder(folder string) bool {
	name, hasPrefix := strings.CutPrefix(folder, "skills/")
	return hasPrefix && ValidName(name)
}

func readProjectSkill(files resource.ProjectFiles, folder, name string) (Document, map[string][]byte, error) {
	prefix := folder + "/"
	for _, local := range files.LocalFiles {
		if strings.HasPrefix(local, prefix) {
			return Document{}, nil, projectError(local, "文件只保存在宿主，未进入 Run 快照")
		}
	}
	markdown, found := files.Files[prefix+"SKILL.md"]
	if !found {
		return Document{}, nil, projectError(prefix+"SKILL.md", "快照内缺少文件")
	}
	document := Document{Markdown: string(markdown)}
	resources := map[string][]byte{}
	placeholderID := uuid.NewString()
	for file, raw := range files.Files {
		rel, found := strings.CutPrefix(file, prefix)
		if !found || rel == "SKILL.md" {
			continue
		}
		if !resource.ValidSkillPath(rel) || len(raw) > MaxResource {
			return document, nil, projectError(file, "Skill 资源路径或大小无效")
		}
		media := mime.TypeByExtension(path.Ext(rel))
		if media == "" {
			media = "application/octet-stream"
		}
		sum := sha256.Sum256(raw)
		document.Assets = append(document.Assets, Asset{Path: rel, ObjectID: placeholderID, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw)), MediaType: media})
		resources[rel] = raw
	}
	slices.SortFunc(document.Assets, func(left, right Asset) int { return strings.Compare(left.Path, right.Path) })
	for _, dir := range files.Directories {
		if rel, found := strings.CutPrefix(dir, prefix); found {
			document.Directories = append(document.Directories, rel)
		}
	}
	slices.Sort(document.Directories)
	if _, err := ValidateDocument("skill", name, document, true); err != nil {
		return document, nil, projectError(folder, "Skill 正文或资源无效")
	}
	return document, resources, nil
}

func sameSkill(left, right Document) bool {
	content := func(doc Document) map[string]string {
		entries := make(map[string]string)
		for _, asset := range doc.Assets {
			entries[asset.Path] = asset.SHA256
			for dir := path.Dir(asset.Path); dir != "."; dir = path.Dir(dir) {
				entries[dir+"/"] = ""
			}
		}
		for _, dir := range doc.Directories {
			for ; dir != "."; dir = path.Dir(dir) {
				entries[dir+"/"] = ""
			}
		}
		return entries
	}
	return left.Markdown == right.Markdown && maps.Equal(content(left), content(right))
}

func (p PublishProject) agentDocument(skills []PublishEntry) Document {
	config := p.config
	config.Context.Normalize()
	config.Skills = make([]SkillRef, 0, len(skills))
	for _, skill := range skills {
		config.Skills = append(config.Skills, SkillRef{ID: skill.ID, Version: skill.Label})
	}
	raw, _ := json.Marshal(config)
	return Document{Markdown: p.markdown, ConfigYAML: string(raw)}
}

func sameAgent(left, right Document) bool {
	var first, second Config
	if left.Markdown != right.Markdown || strictJSON(left.ConfigYAML, &first) != nil || strictJSON(right.ConfigYAML, &second) != nil {
		return false
	}
	first.Context.Normalize()
	second.Context.Normalize()
	if len(first.Skills) == 0 && len(second.Skills) == 0 {
		first.Skills, second.Skills = nil, nil
	}
	if len(first.Tools) == 0 && len(second.Tools) == 0 {
		first.Tools, second.Tools = nil, nil
	}
	return Equal(first, second)
}
