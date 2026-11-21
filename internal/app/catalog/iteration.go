package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource"
)

var ErrIterationDraft = errors.New("iteration_draft_changed")

type IterationVersionUnavailable struct {
	Kind, Name string
	Archived   bool
}

func (e *IterationVersionUnavailable) Error() string {
	if e.Archived {
		return fmt.Sprintf("%s %s 已归档，请先恢复后再开始迭代。", e.Kind, e.Name)
	}
	return fmt.Sprintf("%s %s 没有可用于迭代的正式版本。", e.Kind, e.Name)
}

type formalVersions interface {
	LatestFormalVersion(context.Context, string, string, string, string) (Version, bool, error)
}

func (s *Service) ResolveIteration(ctx context.Context, actor, space, id string) (*run.Creativity, error) {
	repo, ok := s.Repo.(formalVersions)
	if !ok {
		return nil, identity.ErrNotFound
	}
	draft, err := s.Draft(ctx, actor, space, "agent", id)
	if err != nil {
		return nil, err
	}
	version, published, err := repo.LatestFormalVersion(ctx, actor, space, "agent", id)
	if err != nil {
		return nil, err
	}
	base := &run.Creativity{TargetAgentID: id, TargetVersionID: version.ID, TargetDraftRevision: draft.Revision}
	var skills []string
	if version.ID != "" {
		if err := s.loadVersion(ctx, actor, space, "agent", &version); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(version.Bundle.Document.ConfigYAML)) {
			return nil, ErrConfig
		}
		for _, binding := range version.Bundle.Bindings {
			skills = append(skills, binding.SkillID)
		}
	} else if published {
		return nil, &IterationVersionUnavailable{Kind: "Agent", Name: draft.Name}
	} else {
		config, err := ValidateDocument("agent", draft.Name, draft.Document, false)
		if err != nil {
			return nil, err
		}
		if config != nil {
			for _, skill := range config.Skills {
				skills = append(skills, skill.ID)
			}
		}
	}
	for _, skill := range skills {
		version, _, err := repo.LatestFormalVersion(ctx, actor, space, "skill", skill)
		if err != nil && !errors.Is(err, identity.ErrNotFound) {
			return nil, err
		}
		if version.ID == "" {
			unavailable := &IterationVersionUnavailable{Kind: "Skill", Name: skill}
			if draft, err := s.Repo.CatalogDraft(ctx, actor, space, "skill", skill); err == nil {
				unavailable.Name, unavailable.Archived = draft.Name, draft.Archived
			}
			return nil, unavailable
		}
		base.SkillBases = append(base.SkillBases, run.SkillBase{SkillID: skill, VersionID: version.ID})
	}
	return base, nil
}

type projectSkill struct {
	run.SkillBase
	Path string `json:"path,omitempty"`
}

func (s *Service) RestoreIteration(ctx context.Context, actor, space string, base run.Creativity, write func(string, []byte) error) error {
	var document Document
	if base.TargetVersionID == "" {
		draft, err := s.Draft(ctx, actor, space, "agent", base.TargetAgentID)
		if err != nil {
			return err
		}
		if draft.Revision != base.TargetDraftRevision {
			return ErrIterationDraft
		}
		document = draft.Document
		if _, err := ValidateDocument("agent", draft.Name, document, false); err != nil {
			return err
		}
	} else {
		version, err := s.Version(ctx, actor, space, "agent", base.TargetAgentID, base.TargetVersionID)
		if err != nil {
			return err
		}
		document = version.Bundle.Document
		if !json.Valid([]byte(document.ConfigYAML)) {
			return ErrConfig
		}
	}
	if err := write("AGENTS.md", []byte(document.Markdown)); err != nil {
		return err
	}
	if err := write(resource.AgentConfigPath, []byte(document.ConfigYAML)); err != nil {
		return err
	}
	project := struct {
		Skills []projectSkill `json:"skills"`
	}{Skills: make([]projectSkill, 0, len(base.SkillBases))}
	for _, skill := range base.SkillBases {
		folder, err := s.restoreIterationSkill(ctx, actor, space, skill, write)
		if err != nil {
			return err
		}
		project.Skills = append(project.Skills, projectSkill{SkillBase: skill, Path: folder})
	}
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return err
	}
	return write(resource.ProjectBindingsPath, data)
}

func (s *Service) restoreIterationSkill(ctx context.Context, actor, space string, base run.SkillBase, write func(string, []byte) error) (string, error) {
	version, err := s.Repo.CatalogVersion(ctx, actor, space, "skill", base.SkillID, base.VersionID)
	if err != nil {
		return "", err
	}
	object, file, err := s.Content.Read(ctx, actor, space, version.ContentID)
	if err != nil {
		return "", err
	}
	defer file.Close()
	bundle, err := decodeSkillBundle(object, file, version.SHA256)
	if err != nil {
		return "", err
	}
	if bundle.DefinitionID != base.SkillID || !ValidName(bundle.Name) {
		return "", storage.ErrIntegrity
	}
	if _, err := ValidateDocument("skill", bundle.Name, bundle.Document, true); err != nil {
		return "", err
	}
	folder := path.Join("skills", bundle.Name)
	if err := write(path.Join(folder, "SKILL.md"), []byte(bundle.Document.Markdown)); err != nil {
		return "", err
	}
	for _, dir := range bundle.Document.Directories {
		if err := write(path.Join(folder, dir), nil); err != nil {
			return "", err
		}
	}
	zipped, err := resource.OpenSkillPackage(file, object)
	if err != nil {
		return "", err
	}
	for _, asset := range bundle.Document.Assets {
		data, err := resource.ReadSkillEntry(zipped, asset.Path, MaxResource)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != asset.Size || hex.EncodeToString(digest[:]) != asset.SHA256 {
			return "", storage.ErrIntegrity
		}
		if err := write(path.Join(folder, asset.Path), data); err != nil {
			return "", err
		}
	}
	return folder, nil
}
