-- name: InsertSecret :exec
INSERT INTO secrets (id, name, kind, metadata, current_version_id, created_at, updated_at)
VALUES (@id, @name, @kind, @metadata, @current_version_id, @now, @now);

-- name: InsertSecretVersion :exec
INSERT INTO secret_versions (id, secret_id, version_number, payload_keys, encryption_scheme, key_id, encrypted_dek,
                             encrypted_dek_nonce, nonce, ciphertext, created_at)
VALUES (@id, @secret_id, @version_number, @payload_keys, @encryption_scheme, @key_id, @encrypted_dek,
        @encrypted_dek_nonce, @nonce, @ciphertext, @now);

-- name: GetSecretByName :one
SELECT * FROM secrets WHERE name = @name;

-- name: ListSecrets :many
SELECT * FROM secrets ORDER BY name;

-- name: GetSecretVersion :one
SELECT * FROM secret_versions WHERE id = @id;

-- name: SetCurrentSecretVersion :exec
UPDATE secrets SET current_version_id = @current_version_id, updated_at = @now WHERE id = @id;

-- name: DeleteSecret :exec
DELETE FROM secrets WHERE id = @id;

-- name: ListSecretVersions :many
SELECT v.*, s.kind AS secret_kind FROM secret_versions v JOIN secrets s ON s.id = v.secret_id
ORDER BY v.secret_id, v.version_number;

-- name: RewrapSecretVersion :exec
UPDATE secret_versions SET key_id = @key_id, encrypted_dek = @encrypted_dek,
                           encrypted_dek_nonce = @encrypted_dek_nonce
WHERE id = @id;
