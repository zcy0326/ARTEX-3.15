package server

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/Autumn-27/artex/workproduct"
)

func (s *Server) taskWorkResults(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if r.URL.Query().Get("before") != "" && (err != nil || before < 0) {
		writeErr(w, 400, "before 无效")
		return
	}
	records, more, err := t.Store.WorkRecords(before, 30)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := map[string]any{"records": records, "has_more": more}
	if more && len(records) > 0 {
		out["next_before"] = records[len(records)-1].ID
	}
	writeJSON(w, 200, out)
}

func (s *Server) taskWorkList(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	nodes, err := t.Store.ListByKind("intent", 200)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"work": nodes})
}

func (s *Server) taskResultFile(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	resultID, e1 := strconv.ParseInt(r.PathValue("result"), 10, 64)
	index, e2 := strconv.Atoi(r.PathValue("file"))
	if e1 != nil || e2 != nil || index < 0 {
		writeErr(w, 400, "产物 ID 无效")
		return
	}
	n, err := t.Store.GetNode(resultID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n == nil || n.Kind != "fact" {
		writeErr(w, 404, "result not found")
		return
	}
	var result struct {
		RecordType string `json:"record_type"`
		Files      []workproduct.Artifact
	}
	if json.Unmarshal(n.Payload, &result) != nil || result.RecordType != "result" || index >= len(result.Files) {
		writeErr(w, 404, "file not found")
		return
	}
	artifact := result.Files[index]
	f, err := workproduct.OpenVerified(filepath.Join(s.m.dir, "tasks", t.ID), artifact)
	if err != nil {
		writeErr(w, http.StatusConflict, "产物缺失或版本校验失败，不能下载旧引用")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("ETag", fmt.Sprintf(`"%s"`, artifact.SHA256))
	http.ServeContent(w, r, artifact.Name, info.ModTime(), f)
}
