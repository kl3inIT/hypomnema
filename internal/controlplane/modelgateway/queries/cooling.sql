-- name: RecordModelCooling :exec
INSERT INTO gateway_model_cooling (route, model, observed_at, resets_at)
VALUES (@route, @model, @observed_at, @resets_at)
ON CONFLICT (route, model) DO UPDATE SET
    observed_at = excluded.observed_at, resets_at = excluded.resets_at
WHERE excluded.observed_at >= gateway_model_cooling.observed_at;
