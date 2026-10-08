-- name: CreateVerificationCode :one
INSERT INTO verification_code (email, code, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetLatestVerificationCode :one
SELECT * FROM (
  SELECT * FROM verification_code WHERE email = $1
  ORDER BY created_at DESC, id DESC LIMIT 1
) AS latest
WHERE used = FALSE
  AND expires_at > now()
  AND attempts < 5;

-- name: LockVerificationCodeEmail :exec
SELECT pg_advisory_xact_lock(hashtextextended('multica-verification:' || sqlc.arg(email)::text, 0));

-- name: InvalidateVerificationCodesByEmail :exec
UPDATE verification_code SET used = TRUE WHERE email = $1 AND used = FALSE;

-- name: MarkVerificationCodeUsed :exec
UPDATE verification_code
SET used = TRUE
WHERE id = $1;

-- name: IncrementVerificationCodeAttempts :exec
UPDATE verification_code
SET attempts = attempts + 1
WHERE id = $1;

-- name: GetLatestCodeByEmail :one
SELECT * FROM verification_code
WHERE email = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: DeleteExpiredVerificationCodes :exec
DELETE FROM verification_code
WHERE expires_at < now() - interval '1 hour';
