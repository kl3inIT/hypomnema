package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Profiles, providers and models (issue #42). Every token reads them, so a
// coordinator sees its choices. The owner applies a profile, and the Bot
// coordinator from its chat (setupchanges.go); a provider, and deleting a
// profile, only the owner.

// ListProfiles returns every profile at its current version.
func (s *Server) ListProfiles(ctx context.Context, _ openapi.ListProfilesRequestObject,
) (openapi.ListProfilesResponseObject, error) {
	profiles, err := s.svc.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}

	return openapi.ListProfiles200JSONResponse(toProfileList(profiles)), nil
}

// GetProfile returns a profile at its current version.
func (s *Server) GetProfile(ctx context.Context, req openapi.GetProfileRequestObject,
) (openapi.GetProfileResponseObject, error) {
	p, err := s.svc.GetProfile(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	out := toProfile(p)
	out.ToolAccess, err = s.toolAccess(ctx, nil, p.Runtime, p.ToolSwitches())
	if err != nil {
		return nil, err
	}

	return openapi.GetProfile200JSONResponse(out), nil
}

// ApplyProfile stores a profile's YAML: 201 for a new version, 200 when the
// source equals the current one.
func (s *Server) ApplyProfile(ctx context.Context, req openapi.ApplyProfileRequestObject,
) (openapi.ApplyProfileResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	var (
		p       orchestration.Profile
		created bool
	)

	if chat == "" {
		p, created, err = s.svc.ApplyProfile(ctx, req.Name, req.Body.Source)
	} else {
		p, created, err = s.svc.ApplyProfileFromChat(ctx, chat, req.Name, req.Body.Source)
	}

	if err != nil {
		return nil, err
	}

	if created {
		return openapi.ApplyProfile201JSONResponse(toProfile(p)), nil
	}

	return openapi.ApplyProfile200JSONResponse(toProfile(p)), nil
}

// RestoreProfileVersion applies one of a profile's versions again, as ApplyProfile: the owner, or the main Bot from
// its chat.
func (s *Server) RestoreProfileVersion(ctx context.Context, req openapi.RestoreProfileVersionRequestObject,
) (openapi.RestoreProfileVersionResponseObject, error) {
	chat, err := setupChat(principal(ctx))
	if err != nil {
		return nil, err
	}

	p, created, err := s.svc.RestoreProfileVersion(ctx, chat, req.Name, req.VersionId)
	if err != nil {
		return nil, err
	}

	if created {
		return openapi.RestoreProfileVersion201JSONResponse(toProfile(p)), nil
	}

	return openapi.RestoreProfileVersion200JSONResponse(toProfile(p)), nil
}

// DeleteProfile deletes a profile; only the owner (policy.go). One never used
// goes, one used before is marked deleted (issue #485).
func (s *Server) DeleteProfile(ctx context.Context, req openapi.DeleteProfileRequestObject,
) (openapi.DeleteProfileResponseObject, error) {
	if err := s.svc.DeleteProfile(ctx, req.Name); err != nil {
		return nil, err
	}

	return openapi.DeleteProfile204Response{}, nil
}

// ListProfileVersions returns a profile's versions, newest first.
func (s *Server) ListProfileVersions(ctx context.Context, req openapi.ListProfileVersionsRequestObject,
) (openapi.ListProfileVersionsResponseObject, error) {
	versions, err := s.svc.ListProfileVersions(ctx, req.Name)
	if err != nil {
		return nil, err
	}

	return openapi.ListProfileVersions200JSONResponse(toProfileList(versions)), nil
}

// ListProviders returns the providers with their models.
func (s *Server) ListProviders(ctx context.Context, _ openapi.ListProvidersRequestObject,
) (openapi.ListProvidersResponseObject, error) {
	providers, err := s.svc.ListProviders(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListProviders200JSONResponse{Data: make([]openapi.Provider, 0, len(providers))}
	for _, p := range providers {
		out.Data = append(out.Data, toProvider(p))
	}

	return out, nil
}

// ApplyProvider stores a provider's YAML and its models.
func (s *Server) ApplyProvider(ctx context.Context, req openapi.ApplyProviderRequestObject,
) (openapi.ApplyProviderResponseObject, error) {
	p, err := s.svc.ApplyProvider(ctx, req.Name, req.Body.Source)
	if err != nil {
		return nil, err
	}

	return openapi.ApplyProvider200JSONResponse(toProvider(p)), nil
}

// DeleteProvider removes a provider and its models; only the owner (policy.go, issue #610).
func (s *Server) DeleteProvider(ctx context.Context, req openapi.DeleteProviderRequestObject,
) (openapi.DeleteProviderResponseObject, error) {
	if err := s.svc.DeleteProvider(ctx, req.Name); err != nil {
		return nil, err
	}

	return openapi.DeleteProvider204Response{}, nil
}

// ListModels returns every model with its provider.
func (s *Server) ListModels(ctx context.Context, _ openapi.ListModelsRequestObject,
) (openapi.ListModelsResponseObject, error) {
	models, err := s.svc.ListModels(ctx)
	if err != nil {
		return nil, err
	}

	out := openapi.ListModels200JSONResponse{Data: make([]openapi.Model, 0, len(models))}
	for _, m := range models {
		out.Data = append(out.Data, openapi.Model{Provider: m.Provider, Name: m.Name, Kind: m.Kind})
	}

	return out, nil
}

func toProfile(p orchestration.Profile) openapi.Profile {
	out := openapi.Profile{
		Name: p.Name, Description: p.Description, VersionId: p.VersionID, Version: p.Version, Runtime: p.Runtime,
		Provider: ptr(p.Provider),
		Model:    ptr(p.Model), Effort: ptr(p.Effort), Instruction: ptr(p.Instruction), Source: p.Source,
		Fallbacks: append([]string{}, p.Fallbacks...), Skills: append([]string{}, p.Skills...),
		SkillsOff: append([]string{}, p.SkillsOff...), Tools: append([]string{}, p.Tools...),
		Preset: openapi.ToolPreset(p.Preset), ToolsOff: append([]string{}, p.ToolsOff...), CreatedAt: p.CreatedAt,
		CpuRequestMillis: ptr(p.CPURequestMillis), MemoryRequestBytes: ptr(p.MemoryRequestBytes),
	}

	if a := p.Availability; a != nil {
		out.Available = &a.Available
		out.AvailableAt = a.At

		if a.Reason != "" {
			reason := openapi.ProfileUnavailableReason(a.Reason)
			out.UnavailableReason = &reason
		}
	}

	return out
}

func toProfileList(profiles []orchestration.Profile) openapi.ProfileList {
	out := openapi.ProfileList{Data: make([]openapi.Profile, 0, len(profiles))}
	for _, p := range profiles {
		out.Data = append(out.Data, toProfile(p))
	}

	return out
}

func toProvider(p orchestration.Provider) openapi.Provider {
	return openapi.Provider{
		Name: p.Name, Kind: openapi.ProviderKind(p.Kind), BaseUrl: ptr(p.BaseURL), ApiKeyRef: ptr(p.APIKeyRef),
		Models: p.Models, Source: p.Source, UpdatedAt: p.UpdatedAt,
	}
}

// ListTools returns Hypomnema's tools by family (issue #536): the names and
// families a profile's or a Bot's tools and tools_off take.
func (s *Server) ListTools(context.Context, openapi.ListToolsRequestObject) (openapi.ListToolsResponseObject, error) {
	out := openapi.ListTools200JSONResponse{Data: make([]openapi.ToolFamily, 0, len(agenttool.ToolFamilies))}
	for _, f := range agenttool.ToolFamilies {
		out.Data = append(out.Data, openapi.ToolFamily{
			Name: f.Name, Description: f.Description, Pattern: f.Pattern, Tools: append([]string{}, f.Tools...),
		})
	}

	return out, nil
}
