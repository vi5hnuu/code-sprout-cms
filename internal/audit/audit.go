// Package audit records who changed what. Entries are written in the same transaction as the
// change, so the log cannot claim something happened that was rolled back (or miss one that
// committed).
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"code-sprout-cms/internal/audit/auditdb"
)

type Entry struct {
	ActorID  int64  // 0 = the system (e.g. the scheduled publisher)
	Action   string // <entity>.<verb>, e.g. category.move, content.publish
	Entity   string
	EntityID int64
	Data     any // small JSON-able detail: before/after of the fields that changed
}

func Record(ctx context.Context, tx auditdb.DBTX, e Entry) error {
	data, err := json.Marshal(e.Data)
	if err != nil {
		return fmt.Errorf("audit %s: %w", e.Action, err)
	}
	if e.Data == nil {
		data = []byte("{}")
	}
	var actor *int64
	if e.ActorID != 0 {
		actor = &e.ActorID
	}
	return auditdb.New(tx).InsertAudit(ctx, auditdb.InsertAuditParams{
		ActorID: actor, Action: e.Action, Entity: e.Entity, EntityID: &e.EntityID, Data: data,
	})
}
