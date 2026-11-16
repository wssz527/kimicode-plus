package bridge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

const blobQuota = 64 << 20

func (s *Service) storeBlob(ctx context.Context, att Attachment, data []byte) (Attachment, error) {
	if len(data) == 0 {
		return att, apiError(422, "ATTACHMENT_EMPTY", "暂不支持零字节附件")
	}
	hash := sha256.Sum256(data)
	if len(data) > maxAttachment || int64(len(data)) != att.Size || hex.EncodeToString(hash[:]) != att.SHA256 {
		return att, apiError(422, "ATTACHMENT_MISMATCH", "附件字节、大小或 SHA-256 不一致")
	}
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT ref FROM attachment_blobs WHERE sha256=?`, att.SHA256).Scan(&att.BlobRef); err == nil {
			return nil
		} else if err != sql.ErrNoRows {
			return err
		}
		var used int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size),0) FROM attachment_blobs`).Scan(&used); err != nil {
			return err
		}
		if used+att.Size > blobQuota {
			return apiError(413, "ATTACHMENT_QUOTA", "附件存储达到 64 MiB 限额；请清理未引用附件")
		}
		att.BlobRef = "blob-" + security.Random()
		_, err := tx.ExecContext(ctx, `INSERT INTO attachment_blobs(ref,sha256,size,mime,data,created_at) VALUES(?,?,?,?,?,?)`, att.BlobRef, att.SHA256, att.Size, att.MIME, data, now())
		return err
	})
	if err != nil {
		return att, err
	}
	att.DataBase64 = ""
	att.Status = "STORED"
	att.Name = redact.Text(att.Name)
	att.SourceURL = redact.Text(att.SourceURL)
	return att, nil
}

func (s *Service) resolveAttachments(ctx context.Context, attachments []Attachment) ([]Attachment, []string, error) {
	out := make([]Attachment, 0, len(attachments))
	var findings []string
	for _, att := range attachments {
		var data []byte
		if att.DataBase64 != "" {
			var err error
			data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(att.DataBase64))
			if err != nil {
				return nil, nil, apiError(422, "ATTACHMENT_INVALID", "附件 base64 无效")
			}
			var errStore error
			att, errStore = s.storeBlob(ctx, att, data)
			if errStore != nil {
				return nil, nil, errStore
			}
		} else if att.BlobRef != "" {
			var size int64
			var hash string
			err := s.db.QueryRowContext(ctx, `SELECT sha256,size,data FROM attachment_blobs WHERE ref=?`, att.BlobRef).Scan(&hash, &size, &data)
			if err == sql.ErrNoRows {
				return nil, nil, apiError(422, "ATTACHMENT_UNAVAILABLE", "附件 blob 不存在或已回收")
			}
			if err != nil {
				return nil, nil, err
			}
			if hash != att.SHA256 || size != att.Size {
				return nil, nil, apiError(422, "ATTACHMENT_MISMATCH", "附件引用与摘要不一致")
			}
			att.Status = "STORED"
		} else {
			att.Status = "UNAVAILABLE"
		}
		scan := data
		if len(scan) > 256<<10 {
			scan = scan[:256<<10]
		}
		findings = append(findings, redact.Findings(string(scan))...)
		findings = append(findings, redact.Findings(att.Name+"\n"+att.SourceURL)...)
		att.Name = redact.Text(att.Name)
		att.SourceURL = redact.Text(att.SourceURL)
		att.DataBase64 = ""
		out = append(out, att)
	}
	return out, redact.JoinFindings(findings), nil
}

func (s *Service) attachmentReader(rowID string, attachments []Attachment) func(context.Context, string) ([]byte, error) {
	allowed := map[string]bool{}
	for _, a := range attachments {
		if a.Status == "STORED" {
			allowed[a.BlobRef] = true
		}
	}
	return func(ctx context.Context, ref string) ([]byte, error) {
		if !allowed[ref] {
			return nil, apiError(403, "ATTACHMENT_SCOPE", "附件不属于当前获准任务")
		}
		var data []byte
		err := s.db.QueryRowContext(ctx, `SELECT b.data FROM attachment_blobs b JOIN task_attachment_refs r ON r.blob_ref=b.ref WHERE r.task_row_id=? AND b.ref=?`, rowID, ref).Scan(&data)
		return data, err
	}
}

func transportAttachments(attachments []Attachment) []desktop.Blob {
	out := []desktop.Blob{}
	for _, a := range attachments {
		if a.Status == "STORED" {
			out = append(out, desktop.Blob{Status: "STORED", Ref: a.BlobRef, SHA256: a.SHA256, Size: a.Size, MIME: a.MIME})
		}
	}
	return out
}

func (s *Service) handleUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxAttachment+1))
	if err != nil {
		fail(w, apiError(400, "ATTACHMENT_INVALID", "无法读取上传内容"))
		return
	}
	att := Attachment{Name: "upload", MIME: r.Header.Get("X-Attachment-Type"), Size: int64(len(data)), SHA256: r.Header.Get("X-Attachment-SHA256")}
	if att.MIME == "" {
		att.MIME = "application/octet-stream"
	}
	if len(att.MIME) > 200 {
		fail(w, apiError(422, "ATTACHMENT_INVALID", "附件类型过长"))
		return
	}
	att, err = s.storeBlob(r.Context(), att, data)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 201, att)
}

func (s *Service) handleAttachment(w http.ResponseWriter, r *http.Request) {
	record, err := s.loadRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, apiError(404, "TASK_NOT_FOUND", "任务不存在"))
		return
	}
	data, err := s.attachmentReader(record.ID, record.Task.Attachments)(r.Context(), r.PathValue("blob"))
	if err != nil {
		fail(w, apiError(404, "ATTACHMENT_UNAVAILABLE", "附件不属于此任务或已不可用"))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=attachment.bin")
	w.Write(data)
}

func (s *Service) collectAttachments(ctx context.Context, olderThan time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM attachment_blobs WHERE created_at<? AND NOT EXISTS(SELECT 1 FROM task_attachment_refs r WHERE r.blob_ref=attachment_blobs.ref)`, olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Service) handleCollectAttachments(w http.ResponseWriter, r *http.Request) {
	n, err := s.collectAttachments(r.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"removed": n, "retentionHours": 24})
}
