-- name: ListGatewayKeyNamesBySecret :many
SELECT gateway_keys.name FROM gateway_keys
JOIN secret_versions ON secret_versions.id = gateway_keys.secret_version_id
JOIN secrets ON secrets.id = secret_versions.secret_id
WHERE secrets.name = @secret_name
ORDER BY gateway_keys.name;
