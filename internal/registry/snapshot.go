package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/httpx"
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

func (s *Service) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	out, err := s.Snapshot(r.Context(), r.URL.Query().Get("providerId"), r.URL.Query().Get("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, out)
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

func (s *Service) handleMetadataEdit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProviderID string `json:"providerId"`
		ID         string `json:"id"`
		Field      string `json:"field"`
		Value      any    `json:"value"`
		Restore    bool   `json:"restoreAutomatic"`
		Revision   *int64 `json:"revision"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "必须提供编辑基线"))
		return
	}
	out, err := s.editMetadata(r.Context(), req.ProviderID, req.ID, req.Field, req.Value, req.Restore, *req.Revision)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, out)
}

type RollbackPreview struct {
	Revision  int64             `json:"revision"`
	Detail    ModelChangeDetail `json:"detail"`
	Conflicts []string          `json:"conflicts"`
}

func (s *Service) handleRollbackPreview(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}

	before, err := s.Snapshot(r.Context(), "", "")
	if err != nil {
		fail(w, err)
		return
	}
	detail, err := s.ChangeDetail(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	conflicts := []string{}
	for _, m := range detail.Models {
		records, err := s.loadModels(s.db, []modelKey{{m.ProviderID, m.ID}})
		if err != nil {
			fail(w, err)
			return
		}
		var expected *modelRecord
		if m.After != nil {
			raw, _ := json.Marshal(*m.After)
			expected, err = decodeModel(raw)
			if err != nil {
				fail(w, err)
				return
			}
		}
		if !recordEqual(records[modelKey{m.ProviderID, m.ID}], expected) {
			conflicts = append(conflicts, m.ProviderID+"/"+m.ID+": 模型有后续修改")
		}
	}
	for _, p := range detail.Providers {
		conflicted, err := s.providerRollbackConflict(s.db, p)
		if err != nil {
			fail(w, err)
			return
		}
		if conflicted {
			conflicts = append(conflicts, p.ProviderID+": 供应商有后续修改或模型引用")
		}
	}
	after, err := s.Snapshot(r.Context(), "", "")
	if err != nil {
		fail(w, err)
		return
	}
	if after.Revision != before.Revision {
		fail(w, conflict("STALE_REVISION", "预览期间配置变化，请重新预览"))
		return
	}
	httpx.JSON(w, 200, RollbackPreview{before.Revision, detail, conflicts})
}
