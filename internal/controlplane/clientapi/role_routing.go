package clientapi

import (
	"context"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

func (s *Server) GetRoleRouting(ctx context.Context, _ openapi.GetRoleRoutingRequestObject,
) (openapi.GetRoleRoutingResponseObject, error) {
	source, err := s.svc.RoleRouting(ctx)
	if err != nil {
		return nil, err
	}
	return openapi.GetRoleRouting200JSONResponse{Source: source}, nil
}

func (s *Server) PutRoleRouting(ctx context.Context, req openapi.PutRoleRoutingRequestObject,
) (openapi.PutRoleRoutingResponseObject, error) {
	if err := s.svc.ApplyRoleRouting(ctx, req.Body.Source); err != nil {
		return nil, err
	}
	return openapi.PutRoleRouting204Response{}, nil
}

func (s *Server) GetRoleMetrics(ctx context.Context, req openapi.GetRoleMetricsRequestObject,
) (openapi.GetRoleMetricsResponseObject, error) {
	metrics, err := s.svc.RoleMetrics(ctx, val(req.Params.Since))
	if err != nil {
		return nil, err
	}
	out := make(openapi.GetRoleMetrics200JSONResponse, 0, len(metrics))
	for _, metric := range metrics {
		out = append(out, openapi.RoleMetric{
			Role: metric.Role, Kind: openapi.TaskKind(metric.Kind), Runtime: metric.Runtime, Model: metric.Model,
			ChainEntry: metric.ChainEntry, Attempts: metric.Attempts, Succeeded: metric.Succeeded, Failed: metric.Failed,
			CostMicros: metric.CostMicros, Unpriced: metric.Unpriced,
		})
	}
	return out, nil
}
