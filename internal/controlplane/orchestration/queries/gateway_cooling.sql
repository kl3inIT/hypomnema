-- name: ListGatewayModelCooling :many
SELECT * FROM gateway_model_cooling WHERE resets_at > @now;

-- name: GetGatewayModelCooling :one
SELECT * FROM gateway_model_cooling WHERE route = @route AND model = @model;
