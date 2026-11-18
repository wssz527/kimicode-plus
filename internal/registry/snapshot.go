package registry

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"enhancedkimi/internal/storage"
)

type Snapshot struct {
	Revision int64     `json:"revision"`
	Provider *Provider `json:"provider,omitempty"`
	Model    *Model    `json:"model,omitempty"`
}

func (s *Service) Snapshot(ctx context.Context, providerID, modelID string) (Snapshot, error) {
	var out Snapshot
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		out.Revision, err = s.readRevision(tx)
		if err != nil {
			return err
		}
		if providerID == "" {
			return nil
		}
		p, ok, err := s.loadProvider(tx, providerID)
		if err != nil {
			return err
		}
		if !ok {
			return notFound("PROVIDER_NOT_FOUND", "供应商不存在")
		}
		out.Provider = &p
		if modelID != "" {
			records, err := s.loadModels(tx, []modelKey{{providerID, modelID}})
			if err != nil {
				return err
			}
			rec := records[modelKey{providerID, modelID}]
			if rec == nil {
				return notFound("MODEL_NOT_FOUND", "模型不存在")
			}
			m := recordToModel(rec)
			out.Model = &m
		}
		return nil
	})
	return out, err
}


func (s *Service) editMetadata(ctx context.Context, providerID, modelID, field string, value any, restore bool, revision int64) (OverrideResult, error) {
	if err := validateIdentity(providerID, modelID); err != nil {
		return OverrideResult{}, err
	}
	if field != "displayName" && !isKnownField(field) {
		return OverrideResult{}, badRequest("UNKNOWN_FIELD", "未知字段")
	}
	var out OverrideResult
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if current != revision {
			return conflict("STALE_REVISION", "编辑基线 %d 已过期，当前 %d；请关闭并重新读取", revision, current)
		}
		key := modelKey{providerID, modelID}
		records, err := s.loadModels(tx, []modelKey{key})
		if err != nil {
			return err
		}
		rec := records[key]
		if rec == nil {
			return notFound("MODEL_NOT_FOUND", "模型不存在")
		}
		before := rec.clone()
		if field == "displayName" {
			if restore {
				rec.ManualName = nil
			} else {
				name, ok := value.(string)
				if !ok || strings.TrimSpace(name) == "" || len(name) > 200 {
					return badRequest("INVALID_NAME", "显示名应为 1–200 字符")
				}
				name = strings.TrimSpace(name)
				rec.ManualName = &name
			}
			rec.DisplayName = rec.AutoName
			if rec.DisplayName == "" {
				rec.DisplayName = rec.Key.ModelID
			}
			if rec.ManualName != nil {
				rec.DisplayName = *rec.ManualName
			}
		} else if restore {
			c, ok := rec.AutoCapabilities[field]
			if !ok {
				c = Capability{Source: SourceUnknown}
			}
			rec.Capabilities[field] = c
		} else {
			return badRequest("INVALID_EDIT", "使用 override 设置能力值")
		}
		if recordEqual(before, rec) {
			out = OverrideResult{Revision: current, Model: recordToModel(rec), Field: field}
			return nil
		}
		rev, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		delta := modelDelta{key: key, state: "updated", before: before, after: rec}
		id, err := s.writeChange(tx, "metadata", revision, rev, s.now().UTC().Format(time.RFC3339Nano), "更新显示名或恢复自动来源", []modelDelta{delta})
		if err != nil {
			return err
		}
		if err = s.writeModel(tx, rec); err != nil {
			return err
		}
		out = OverrideResult{Revision: rev, ChangeID: id, Model: recordToModel(rec), Field: field, Before: before.Capabilities[field], After: rec.Capabilities[field]}
		return nil
	})
	return out, err
}


type RollbackPreview struct {
	Revision  int64             `json:"revision"`
	Detail    ModelChangeDetail `json:"detail"`
	Conflicts []string          `json:"conflicts"`
}

