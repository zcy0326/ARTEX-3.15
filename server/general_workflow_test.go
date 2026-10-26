package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/workproduct"
)

func TestGeneralWorkflowHTTPAndArtifactAuthorization(t *testing.T) {
	if _, _, err := db.DSN(); err != nil {
		t.Skip(err)
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	s := New(ctx, m, dir, dir, dir)
	s.archiveWG.Wait()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	request := func(method, path string, body any, auth bool) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	host := fmt.Sprintf("https://general-%d.example.test", time.Now().UnixNano())
	create := func(workflow string) *Task {
		t.Helper()
		rec := request("POST", "/api/tasks", map[string]any{"workflow": workflow, "description": host, "goal": "deliver parser", "acceptance": "compare sample output"}, true)
		if rec.Code != 201 {
			t.Fatalf("create %d %s", rec.Code, rec.Body.String())
		}
		var dto TaskDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		task, ok := m.Task(dto.ID)
		if !ok {
			t.Fatal("task missing")
		}
		id, _ := strconv.ParseInt(task.ID, 10, 64)
		t.Cleanup(func() { _ = m.pg.DeleteTask(id) })
		return task
	}
	task := create(db.WorkflowGeneral)
	var assetCount int
	if err = m.pg.QueryRow(`SELECT count(*) FROM task_asset_links WHERE task_id=$1`, task.ID).Scan(&assetCount); err != nil || assetCount != 0 {
		t.Fatalf("general task seeded network target: %d %v", assetCount, err)
	}
	legacy := create("")
	if legacy.Workflow != db.WorkflowSecurity || !legacy.CoverageEnabled {
		t.Fatal("legacy defaults changed")
	}
	var assetID int64
	if err = m.pg.QueryRow(`SELECT asset_id FROM task_asset_links WHERE task_id=$1 LIMIT 1`, legacy.ID).Scan(&assetID); err != nil {
		t.Fatal("legacy target seed missing", err)
	}
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID) })
	if rec := request("POST", "/api/tasks", map[string]string{"workflow": "invalid"}, true); rec.Code != 400 {
		t.Fatalf("invalid mode: %d", rec.Code)
	}
	if !strings.Contains(request("GET", "/api/tasks", nil, true).Body.String(), `"workflow":"general"`) {
		t.Fatal("workflow missing from task DTO")
	}
	root := filepath.Join(m.dir, "tasks", task.ID)
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "result.txt"), []byte("original output"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := workproduct.Capture(t.Context(), root, root, "result.txt")
	if err != nil {
		t.Fatal(err)
	}
	work, err := task.Store.AddWork("produce", "implementer", "checked", "planner", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := task.Store.ClaimIntent(work, "author"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	result, err := task.Store.RecordWorkFact(map[string]any{"record_type": "result", "result_type": "implementation", "summary": "output", "digest": strings.Repeat("a", 64), "files": []workproduct.Artifact{artifact}, "producer_intent_id": work}, work, "author")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/tasks/" + task.ID
	if rec := request("GET", base+"/results", nil, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "output") {
		t.Fatalf("results: %d %s", rec.Code, rec.Body.String())
	}
	if rec := request("GET", base+"/work", nil, true); rec.Code != 200 || !strings.Contains(rec.Body.String(), "implementer") {
		t.Fatalf("work: %d %s", rec.Code, rec.Body.String())
	}
	path := fmt.Sprintf("%s/results/%d/files/0", base, result)
	if rec := request("GET", path, nil, false); rec.Code != 401 {
		t.Fatalf("download without auth: %d", rec.Code)
	}
	if err = os.WriteFile(filepath.Join(root, "result.txt"), []byte("edited original"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := request("GET", path, nil, true); rec.Code != 200 || rec.Body.String() != "original output" || rec.Header().Get("Content-Disposition") == "" {
		t.Fatalf("snapshot download: %d %s", rec.Code, rec.Body.String())
	}
	foreign := fmt.Sprintf("/api/tasks/%s/results/%d/files/0", legacy.ID, result)
	if rec := request("GET", foreign, nil, true); rec.Code != 404 {
		t.Fatalf("cross-task file exposed: %d", rec.Code)
	}
	if err = os.WriteFile(filepath.Join(root, artifact.Snapshot), []byte("tampered snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := request("GET", path, nil, true); rec.Code != http.StatusConflict {
		t.Fatalf("tampered download: %d", rec.Code)
	}
}

func TestGeneralSpawnSchemaPreservesUserSettings(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	old, err := pg.GetTool("spawn_task")
	if err != nil {
		t.Fatal(err)
	}
	if old == nil {
		if err = pg.SeedTool("spawn_task", "fixture", json.RawMessage(`{"type":"object","properties":{}}`), json.RawMessage(`["auto"]`)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if old == nil {
			_, _ = pg.Exec(`DELETE FROM tools WHERE key='spawn_task'`)
			return
		}
		agents, _ := json.Marshal(old.Agents)
		_, _ = pg.Exec(`UPDATE tools SET description=$1,schema=$2,agents=$3,enabled=$4 WHERE key='spawn_task'`, old.Description, string(old.Schema), string(agents), old.Enabled)
	})
	_, err = pg.Exec(`UPDATE tools SET description='user description',enabled=false,agents='[]',schema='{"type":"object","properties":{"description":{"type":"string","default":"user default"}}}' WHERE key='spawn_task'`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	s.refreshGeneralSpawnSchema()
	s.refreshGeneralSpawnSchema()
	row, err := pg.GetTool("spawn_task")
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled || row.Description != "user description" || len(row.Agents) != 0 || !strings.Contains(string(row.Schema), "user default") || !strings.Contains(string(row.Schema), "workflow") || !strings.Contains(string(row.Schema), "acceptance") {
		t.Fatalf("migration overwrote customization: %+v", row)
	}
}

func TestGeneralReuseSchemaPreservesUserSettings(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	for key, property := range map[string]string{"add_work": "inputs", "prepare_worktree": "base_commit"} {
		t.Run(key, func(t *testing.T) {
			old, err := pg.GetTool(key)
			if err != nil {
				t.Fatal(err)
			}
			if old == nil {
				if err = pg.SeedTool(key, "fixture", json.RawMessage(`{"type":"object","properties":{}}`), json.RawMessage(`[]`)); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				if old == nil {
					_, _ = pg.Exec(`DELETE FROM tools WHERE key=$1`, key)
					return
				}
				agents, _ := json.Marshal(old.Agents)
				_, _ = pg.Exec(`UPDATE tools SET description=$2,schema=$3,agents=$4,enabled=$5 WHERE key=$1`, key, old.Description, string(old.Schema), string(agents), old.Enabled)
			})
			_, err = pg.Exec(`UPDATE tools SET description='reuse custom',enabled=false,agents='[]',schema='{"type":"object","properties":{"summary":{"type":"string","default":"custom summary"}}}' WHERE key=$1`, key)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{m: &Manager{pg: pg}}
			s.refreshGeneralSpawnSchema()
			s.refreshGeneralSpawnSchema()
			row, err := pg.GetTool(key)
			if err != nil || row == nil {
				t.Fatal(err)
			}
			var schema struct{ Properties map[string]any }
			if err := json.Unmarshal(row.Schema, &schema); err != nil || schema.Properties[property] == nil {
				t.Fatalf("missing additive %s: %s", property, row.Schema)
			}
			if row.Enabled || row.Description != "reuse custom" || len(row.Agents) != 0 || !strings.Contains(string(row.Schema), "custom summary") {
				t.Fatalf("lost user settings: %+v", row)
			}
			// A user's explicit property definition wins over a later restart.
			custom := `{"type":"string","default":"custom reuse","description":"keep me"}`
			if _, err = pg.Exec(`UPDATE tools SET schema=jsonb_set(schema,ARRAY['properties',$2],$3::jsonb) WHERE key=$1`, key, property, custom); err != nil {
				t.Fatal(err)
			}
			s.refreshGeneralSpawnSchema()
			row, err = pg.GetTool(key)
			if err != nil || !strings.Contains(string(row.Schema), "custom reuse") {
				t.Fatalf("overwrote existing input property: %+v %v", row, err)
			}
		})
	}
}
