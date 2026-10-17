-- name: InsertAudit :exec
INSERT INTO audit_log (actor_id, action, entity, entity_id, data)
VALUES (sqlc.arg(actor_id), sqlc.arg(action), sqlc.arg(entity), sqlc.arg(entity_id), sqlc.arg(data));
