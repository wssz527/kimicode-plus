package desktop

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"enhancedkimi/internal/redact"
)

const (
	attachLogTailBytes = 8 * 1024
	noticesHeadBytes   = 2 * 1024
	noticesFileName    = "THIRD_PARTY_NOTICES.txt"
)

// diagnosticsStatus is the read-only view the settings surface shows for the
// attach log and the open-source notices. Both report their real path and
// availability, so an empty panel is never mistaken for an empty log. Every
// excerpt is redacted: the attach log carries the core URL and pairing material.
func (s *NativeService) diagnosticsStatus() map[string]any {
	return map[string]any{
		"attachLog": diagnoseFile(filepath.Join(s.attachConfig().DataDir, "attach.log"), "tail", attachLogTailBytes),
		"notices":   diagnoseFile(noticesFilePath(), "head", noticesHeadBytes),
	}
}

// noticesFilePath resolves the notices next to the running binary, which is
// where scripts/package.py puts them. No second copy is maintained by hand.
func noticesFilePath() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Join(filepath.Dir(executable), noticesFileName)
}

func diagnoseFile(path, section string, limit int) map[string]any {
	status := map[string]any{"path": path, "available": false}
	if path == "" {
		status["note"] = "无法定位程序所在目录"
		return status
	}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		status["note"] = "尚未生成"
		return status
	case err != nil:
		status["note"] = "无法读取：" + err.Error()
		return status
	case !info.Mode().IsRegular():
		status["note"] = "不是普通文件"
		return status
	}
	excerpt, truncated, err := readExcerpt(path, section, limit)
	if err != nil {
		status["note"] = "无法读取：" + err.Error()
		return status
	}
	status["available"] = true
	status["bytes"] = info.Size()
	status[section] = excerpt
	if truncated {
		status["truncated"] = true
		status["note"] = fmt.Sprintf("内容较长，只显示%s %s。", map[string]string{"tail": "最后", "head": "最前"}[section], sizeText(limit))
	}
	return status
}

func sizeText(limit int) string {
	if limit >= 1024*1024 {
		return fmt.Sprintf("%d MiB", limit/(1024*1024))
	}
	return fmt.Sprintf("%d KiB", limit/1024)
}

// readExcerpt reads one end of the file. A window read keeps a large log from
// being pulled into memory just to display its last few KiB.
func readExcerpt(path, section string, limit int) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	size := info.Size()
	truncated := size > int64(limit)
	length := int64(limit)
	if !truncated {
		length = size
	}
	var raw []byte
	if section == "tail" {
		offset := size - length
		raw = make([]byte, length)
		if _, err := file.ReadAt(raw, offset); err != nil && err != io.EOF {
			return "", false, err
		}
	} else {
		raw = make([]byte, length)
		if _, err := io.ReadFull(file, raw); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", false, err
		}
	}
	text := trimPartialRune(string(raw), section)
	if truncated {
		// A byte window cuts the first or last line in half. Dropping that
		// fragment keeps the excerpt readable without inventing content.
		if section == "tail" {
			if index := strings.IndexByte(text, '\n'); index >= 0 {
				text = text[index+1:]
			}
		} else if index := strings.LastIndexByte(text, '\n'); index >= 0 {
			text = text[:index]
		}
	}
	return redact.Text(text), truncated, nil
}

func trimPartialRune(text, section string) string {
	if utf8.ValidString(text) {
		return text
	}
	for len(text) > 0 && !utf8.ValidString(text) {
		if section == "tail" {
			text = text[1:]
		} else {
			text = text[:len(text)-1]
		}
	}
	return text
}
