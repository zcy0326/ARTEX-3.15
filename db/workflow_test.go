package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGeneralWorkflowDefaults(t *testing.T) {
	for _, value := range []string{"", WorkflowSecurity, " security "} {
		got, err := NormalizeWorkflow(value)
		if err != nil || got != WorkflowSecurity {
			t.Fatal(got, err)
		}
	}
	if _, err := NormalizeWorkflow("unknown"); err == nil {
		t.Fatal("accepted unknown workflow")
	}
}

func generalDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func generalTask(t *testing.T, d *DB, workflow string) *Task {
	t.Helper()
	task, err := d.CreateTaskWithOptions("general fixture", "deliver a parser", TaskCreateOptions{Workflow: workflow, Acceptance: "round trip samples"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	return task
}

func TestGeneralWorkflowPersistsAndTemplatesPreserveMode(t *testing.T) {
	d := generalDB(t)
	legacy := generalTask(t, d, "")
	task := generalTask(t, d, WorkflowGeneral)
	loaded, err := d.GetTask(task.ID)
	if err != nil || loaded.Workflow != WorkflowGeneral || loaded.Acceptance != "round trip samples" || loaded.CoverageEnabled {
		t.Fatalf("round trip: %+v %v", loaded, err)
	}
	if !legacy.CoverageEnabled || legacy.Workflow != WorkflowSecurity {
		t.Fatalf("legacy changed: %+v", legacy)
	}
	template, err := d.CreateTaskTemplate(TaskTemplateInput{Name: fmt.Sprint("general-", time.Now().UnixNano()), Description: "material", Goal: "result", Workflow: WorkflowGeneral, Acceptance: "comparison"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteTaskTemplate(template.ID) })
	name := "renamed-" + template.Name
	changed, err := d.PatchTaskTemplate(template.ID, TaskTemplatePatch{Name: &name})
	if err != nil || changed.Workflow != WorkflowGeneral || changed.Acceptance != "comparison" {
		t.Fatalf("patch lost workflow: %+v %v", changed, err)
	}
	if _, err = d.CreateTaskWithOptions("invalid", "goal", TaskCreateOptions{Workflow: "bogus"}); err == nil {
		t.Fatal("invalid workflow created")
	}
}

func TestGeneralWorkDependenciesAndValidationRevocation(t *testing.T) {
	d := generalDB(t)
	task := generalTask(t, d, WorkflowGeneral)
	other := generalTask(t, d, WorkflowGeneral)
	s := d.Exploration(task.ExplorationID)
	producer, err := s.AddWork("implement", "implementer", "run checks", "planner", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := s.AddWork("verify", "verifier", "independent comparison", "planner", []int64{producer}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(consumer, "early"); err != nil || ok {
		t.Fatalf("early claim %v %v", ok, err)
	}
	ready, err := s.ReadyFrontier(20)
	if err != nil || len(ready) != 1 || ready[0].ID != producer {
		t.Fatalf("ready: %+v %v", ready, err)
	}
	foreign, _ := d.Exploration(other.ExplorationID).AddIntent(map[string]any{"summary": "other"}, 5, nil, "planner")
	if _, err = s.AddWork("bad", "analyst", "check", "planner", []int64{foreign}, nil); err == nil {
		t.Fatal("cross-task dependency accepted")
	}
	if ok, err := s.ClaimIntent(producer, "author"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	result, err := s.RecordWorkFact(map[string]any{"record_type": "result", "digest": "version-1", "summary": "implementation", "producer_intent_id": producer}, producer, "author")
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.GetNode(result)
	if err != nil {
		t.Fatal(err)
	}
	var version struct{ Digest string }
	if err = json.Unmarshal(node.Payload, &version); err != nil || len(version.Digest) != 64 {
		t.Fatal("missing canonical digest", err)
	}
	if err = s.SetNodeState(producer, "done"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(consumer, "reviewer"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	integrate, err := s.AddWork("integrate", "integrator", "final checks", "planner", nil, []int64{result})
	if err != nil {
		t.Fatal(err)
	}
	claimable := func() bool {
		rows, e := s.ReadyFrontier(100)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range rows {
			if r.ID == integrate {
				return true
			}
		}
		return false
	}
	if claimable() {
		t.Fatal("unverified output was ready")
	}
	assessment := func(verdict, digest string) {
		t.Helper()
		_, err := s.RecordWorkFact(map[string]any{"record_type": "validation", "result_id": result, "result_digest": digest, "verdict": verdict, "summary": "assessment"}, consumer, "reviewer")
		if err != nil {
			t.Fatal(err)
		}
	}
	assessment("passed", "wrong-version")
	if claimable() {
		t.Fatal("wrong version accepted")
	}
	assessment("passed", version.Digest)
	if !claimable() {
		t.Fatal("valid assessment did not unlock")
	}
	assessment("failed", version.Digest)
	if claimable() {
		t.Fatal("new failure did not revoke")
	}
	if ok, err := s.ClaimIntent(integrate, "stale"); err != nil || ok {
		t.Fatalf("atomic claim ignored revocation %v %v", ok, err)
	}
	assessment("passed", version.Digest)
	if _, err = d.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, result); err != nil {
		t.Fatal(err)
	}
	if claimable() {
		t.Fatal("removed prerequisite silently satisfied")
	}
}
