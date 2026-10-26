package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func TestGeneralPromptAndToolIsolation(t *testing.T) {
	ctx := WithWorkflow(t.Context(), db.WorkflowGeneral, "a tested parser")
	for _, role := range []string{"analyst", "implementer", "verifier", "integrator"} {
		prompt := generalSystem(ctx, "worker", role)
		if !strings.Contains(prompt, role) || !strings.Contains(prompt, "a tested parser") {
			t.Fatal("missing work contract")
		}
	}
	old := PromptOverride
	t.Cleanup(func() { PromptOverride = old })
	PromptOverride = func(key string) (string, bool) {
		if key == "worker" {
			return "legacy-custom-worker", true
		}
		return "", false
	}
	if !strings.Contains(workerSystem("", "", "", ""), "legacy-custom-worker") {
		t.Fatal("security override changed")
	}
	if strings.Contains(generalSystem(ctx, "worker", "analyst"), "legacy-custom-worker") {
		t.Fatal("security prompt leaked into general")
	}
	if isGeneral(context.Background()) {
		t.Fatal("default workflow changed")
	}
	set := NewToolSet(nil, "")
	worker := map[string]bool{}
	for _, tool := range set.generalTools("worker") {
		worker[tool.Name()] = true
	}
	if !worker["record_result"] || !worker["record_validation"] || worker["add_work"] || worker["report_finding"] {
		t.Fatalf("bad general worker tools: %v", worker)
	}
	for _, tool := range set.WorkerTools() {
		if tool.Name() == "record_result" {
			t.Fatal("legacy worker tool set changed")
		}
	}
	// ToolResolve may inject catalog-bound tools absent from the original base.
	injected := append(set.generalTools("worker"), set.WorkerTools()...)
	for _, tool := range workflowTools(context.Background(), "worker", injected) {
		if tool.Name() == "record_result" || tool.Name() == "add_work" {
			t.Fatal("general catalog injection changed legacy workflow")
		}
	}
	for _, tool := range workflowTools(ctx, "worker", injected) {
		if tool.Name() == "report_finding" || tool.Name() == "insert_assets" {
			t.Fatal("security tools leaked into general defaults")
		}
	}
	settlement := workflowSettlement(ctx, "worker", wrapupSettlement("worker", []string{"Bash"}))
	if !strings.Contains(settlement.Prompt, "record_result") || strings.Contains(settlement.Prompt, "report_finding") {
		t.Fatal("wrong general settlement")
	}
}

func generalCall(t *testing.T, tool actool.CoreTool, input any, fail bool) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(input)
	res, err := tool.Call(t.Context(), raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError != fail {
		t.Fatalf("%s error=%v expected=%v: %s", tool.Name(), res.IsError, fail, res.Flatten())
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Flatten()), &out)
	return out
}

func TestGeneralResultIndependentVersionedValidation(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	task, err := d.CreateTaskWithOptions("fixture", "deliver", db.TaskCreateOptions{Workflow: db.WorkflowGeneral})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	s := d.Exploration(task.ExplorationID)
	root := t.TempDir()
	makeSet := func(role string) *ToolSet {
		t.Helper()
		id, e := s.AddWork(role, role, "check actual output", "planner", nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		if ok, e := s.ClaimIntent(id, role); e != nil || !ok {
			t.Fatal(ok, e)
		}
		set := NewToolSet(s, role)
		set.SetTaskID(task.ID)
		set.runIntentID = id
		set.setupGeneral(WithWorkflow(t.Context(), db.WorkflowGeneral, "checked"), root, root, &db.Node{Payload: json.RawMessage(`{"work_role":"` + role + `"}`)})
		return set
	}
	author := makeSet("implementer")
	verifier := makeSet("verifier")
	if err = os.WriteFile(filepath.Join(root, "code.txt"), []byte("version one"), 0600); err != nil {
		t.Fatal(err)
	}
	result := generalCall(t, author.recordResult(), map[string]any{"summary": "parser", "result_type": "implementation", "confidence": "observed", "evidence": "build output", "files": []string{"code.txt"}}, false)
	id := int64(result["result_id"].(float64))
	digest := result["digest"].(string)
	validation := map[string]any{"result_id": id, "digest": digest, "verdict": "passed", "summary": "checked", "evidence": "independent command and output"}
	generalCall(t, author.recordValidation(), validation, true)
	verifier.runIntentID, author.runIntentID = author.runIntentID, verifier.runIntentID
	generalCall(t, verifier.recordValidation(), validation, true)
	verifier.runIntentID, author.runIntentID = author.runIntentID, verifier.runIntentID
	node, _ := s.GetNode(id)
	if err = tsxGeneralEvidence(verifier, node); err == nil {
		t.Fatal("implementation completed without independent validation")
	}
	validation["digest"] = "wrong"
	generalCall(t, verifier.recordValidation(), validation, true)
	validation["digest"] = digest
	generalCall(t, verifier.recordValidation(), validation, false)
	if err = tsxGeneralEvidence(verifier, node); err != nil {
		t.Fatal(err)
	}
	validation["verdict"] = "failed"
	generalCall(t, verifier.recordValidation(), validation, false)
	if err = tsxGeneralEvidence(verifier, node); err == nil {
		t.Fatal("ignored latest failure")
	}
	validation["verdict"] = "passed"
	generalCall(t, verifier.recordValidation(), validation, false)
	// Original edits create a new snapshot; the earlier verdict must not follow.
	if err = os.WriteFile(filepath.Join(root, "code.txt"), []byte("version two"), 0600); err != nil {
		t.Fatal(err)
	}
	next := generalCall(t, author.recordResult(), map[string]any{"summary": "parser update", "result_type": "implementation", "confidence": "observed", "evidence": "updated source", "files": []string{"code.txt"}}, false)
	nextNode, _ := s.GetNode(int64(next["result_id"].(float64)))
	if next["digest"] == digest || tsxGeneralEvidence(verifier, nextNode) == nil {
		t.Fatal("new version inherited old validation")
	}
	if tsxGeneralEvidence(verifier, node) != nil {
		t.Fatal("original edit damaged old snapshot")
	}
}
