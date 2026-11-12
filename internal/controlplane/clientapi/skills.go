package clientapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/skillsource"
	"github.com/kl3inIT/hypomnema/internal/shared/skillpack"
)

// Skills (issue #21). Every token reads them; only the owner adds, gives to
// everyone and removes one (policy.go).

// SkillSources fetches a skill from a repository.
type SkillSources interface {
	Fetch(ctx context.Context, src skillsource.Source) (skillpack.Skill, string, error)
}

// errNoSkillSource is a control plane that wired no way to reach GitHub.
var errNoSkillSource = errors.New("this control plane does not fetch skills from repositories")

// WithSkillSources lets the owner add a skill from a repository.
func (s *Server) WithSkillSources(src SkillSources) *Server {
	s.skillSources = src

	return s
}

// ListSkills returns every skill at its newest revision.
func (s *Server) ListSkills(ctx context.Context, _ openapi.ListSkillsRequestObject,
) (openapi.ListSkillsResponseObject, error) {
	skills, err := s.svc.ListSkills(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListSkills200JSONResponse{Data: make([]openapi.Skill, 0, len(skills))}
	for _, sk := range skills {
		out.Data = append(out.Data, toSkill(sk))
	}

	return out, nil
}

// AddSkill keeps a skill from a public GitHub repository.
func (s *Server) AddSkill(ctx context.Context, req openapi.AddSkillRequestObject,
) (openapi.AddSkillResponseObject, error) {
	if s.skillSources == nil {
		return nil, errNoSkillSource
	}

	src, err := skillsource.Parse(req.Body.Github)
	if err != nil {
		return nil, err
	}

	sk, commit, err := s.skillSources.Fetch(ctx, src)
	if err != nil {
		return nil, err
	}

	kept, added, err := s.svc.AddSkill(ctx, sk, orchestration.SkillSource{Source: src.String(), Commit: commit})
	if err != nil {
		return nil, err
	}

	if added {
		return openapi.AddSkill201JSONResponse(toSkill(kept)), nil
	}

	return openapi.AddSkill200JSONResponse(toSkill(kept)), nil
}

// UploadSkill keeps a skill given as an archive.
func (s *Server) UploadSkill(ctx context.Context, req openapi.UploadSkillRequestObject,
) (openapi.UploadSkillResponseObject, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("reading the archive: %w", err)
	}

	sk, err := skillpack.FromArchive(data)
	if err != nil {
		return nil, err
	}

	kept, added, err := s.svc.AddSkill(ctx, sk, orchestration.SkillSource{Source: orchestration.SkillUpload})
	if err != nil {
		return nil, err
	}

	if added {
		return openapi.UploadSkill201JSONResponse(toSkill(kept)), nil
	}

	return openapi.UploadSkill200JSONResponse(toSkill(kept)), nil
}

// GetSkill returns a skill with every revision.
func (s *Server) GetSkill(ctx context.Context, req openapi.GetSkillRequestObject,
) (openapi.GetSkillResponseObject, error) {
	sk, err := s.svc.GetSkill(ctx, req.Skill)
	if err != nil {
		return nil, err
	}

	out := toSkill(sk)
	revs := make([]openapi.SkillRevision, len(sk.Revisions))

	for i, r := range sk.Revisions {
		revs[i] = toSkillRevision(r)
	}

	out.Revisions = &revs

	return openapi.GetSkill200JSONResponse(out), nil
}

// DeleteSkill removes a skill and its revisions.
func (s *Server) DeleteSkill(ctx context.Context, req openapi.DeleteSkillRequestObject,
) (openapi.DeleteSkillResponseObject, error) {
	if err := s.svc.DeleteSkill(ctx, req.Skill); err != nil {
		return nil, err
	}

	return openapi.DeleteSkill204Response{}, nil
}

// SetSkillEveryone gives a skill to everyone, or takes it back.
func (s *Server) SetSkillEveryone(ctx context.Context, req openapi.SetSkillEveryoneRequestObject,
) (openapi.SetSkillEveryoneResponseObject, error) {
	sk, err := s.svc.SetSkillEveryone(ctx, req.Skill, req.Body.Everyone)
	if err != nil {
		return nil, err
	}

	return openapi.SetSkillEveryone200JSONResponse(toSkill(sk)), nil
}

func toSkill(sk orchestration.Skill) openapi.Skill {
	return openapi.Skill{
		Name: sk.Name, Everyone: sk.Everyone, Latest: toSkillRevision(sk.Latest),
		CreatedAt: sk.CreatedAt, UpdatedAt: sk.UpdatedAt,
	}
}

func toSkillRevision(r orchestration.SkillRevision) openapi.SkillRevision {
	return openapi.SkillRevision{
		Revision: r.Revision, Description: r.Description, Digest: r.Digest, Size: r.Size, Files: r.Files,
		Source: r.Source, Commit: ptr(r.Commit), CreatedAt: r.CreatedAt,
	}
}

// ListBotSkills returns the skills a Bot gets now.
func (s *Server) ListBotSkills(ctx context.Context, req openapi.ListBotSkillsRequestObject,
) (openapi.ListBotSkillsResponseObject, error) {
	if err := botAllowed(principal(ctx), req.Name); err != nil {
		return nil, err
	}

	given, err := s.svc.BotSkills(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	out := openapi.ListBotSkills200JSONResponse{Data: make([]openapi.SkillRef, 0, len(given))}
	for _, sk := range given {
		out.Data = append(out.Data, openapi.SkillRef{Name: sk.Name, Revision: sk.Revision, Digest: sk.Digest})
	}

	return out, nil
}

// GetSkillArchive returns one revision of a skill as its archive.
func (s *Server) GetSkillArchive(ctx context.Context, req openapi.GetSkillArchiveRequestObject,
) (openapi.GetSkillArchiveResponseObject, error) {
	archive, err := s.svc.SkillArchive(ctx, req.Skill, req.Revision)
	if err != nil {
		return nil, err
	}

	return openapi.GetSkillArchive200ApplicationgzipResponse{
		Body: bytes.NewReader(archive), ContentLength: int64(len(archive)),
	}, nil
}

// BuiltinSkill is a skill the control plane's binary ships.
type BuiltinSkill struct {
	Name        string
	Description string
}

// WithBuiltinSkills says where the skills the binary ships are listed from.
func (s *Server) WithBuiltinSkills(list func() ([]BuiltinSkill, error)) *Server {
	s.builtinSkills = list

	return s
}

// ListBuiltinSkills returns the skills the binary ships.
func (s *Server) ListBuiltinSkills(_ context.Context, _ openapi.ListBuiltinSkillsRequestObject,
) (openapi.ListBuiltinSkillsResponseObject, error) {
	out := openapi.ListBuiltinSkills200JSONResponse{Data: []openapi.BuiltinSkill{}}
	if s.builtinSkills == nil {
		return out, nil
	}

	skills, err := s.builtinSkills()
	if err != nil {
		return nil, err
	}

	for _, sk := range skills {
		out.Data = append(out.Data, openapi.BuiltinSkill{Name: sk.Name, Description: sk.Description})
	}

	return out, nil
}

// ListSkillFiles returns the files of one revision of a skill.
func (s *Server) ListSkillFiles(ctx context.Context, req openapi.ListSkillFilesRequestObject,
) (openapi.ListSkillFilesResponseObject, error) {
	archive, err := s.svc.SkillArchive(ctx, req.Skill, req.Revision)
	if err != nil {
		return nil, err
	}

	sk, err := skillpack.FromArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("reading skill %s revision %d: %w", req.Skill, req.Revision, err)
	}

	out := openapi.ListSkillFiles200JSONResponse{Data: make([]openapi.SkillFile, 0, len(sk.Files))}
	for _, f := range sk.Files {
		out.Data = append(out.Data, openapi.SkillFile{Path: f.Path, Size: int64(len(f.Data)), Executable: f.Exec})
	}

	return out, nil
}
