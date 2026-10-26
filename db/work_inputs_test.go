package db

import (
	"encoding/json"
	"testing"
)

func TestGeneralReuseInputsProvenanceAndSourceDrift(t *testing.T) {
	d := generalDB(t)
	source := generalTask(t, d, WorkflowSecurity)
	unrelated := generalTask(t, d, WorkflowGeneral)
	task, err := d.CreateTaskWithOptions("adapt existing project", "deliver a checked change", TaskCreateOptions{
		Workflow: WorkflowGeneral, SourceTaskIDs: []int64{source.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	s := d.Exploration(task.ExplorationID)
	finding, err := d.Exploration(source.ExplorationID).AddNode(KindFinding, map[string]any{"summary": "controlled PoC observation", "confidence": "observed"}, 5, "confirmed", "source", nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := s.AddNode(KindFact, map[string]any{"summary": "format model", "confidence": "inferred"}, 5, "confirmed", "analyst", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := d.Exploration(unrelated.ExplorationID).AddNode(KindFact, map[string]any{"summary": "private"}, 5, "confirmed", "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []WorkInput{
		{Kind: "finding", NodeID: finding, Purpose: "reproduce the applicable baseline before adapting"},
		{Kind: "reverse_analysis", NodeID: local, Purpose: "check the proposed layout against samples"},
		{Kind: "repository", Locator: "/existing/project", Revision: "declared-commit", Purpose: "extend the existing parser"},
	}
	work, err := s.AddWorkWithInputs("adapt", "implementer", "preserve baseline and pass new samples", "planner", nil, nil, inputs)
	if err != nil {
		t.Fatal(err)
	}
	readInputs := func(id int64) []WorkInput {
		t.Helper()
		node, err := s.GetNode(id)
		if err != nil || node == nil {
			t.Fatal(err)
		}
		var record struct{ Inputs []WorkInput }
		if err = json.Unmarshal(node.Payload, &record); err != nil {
			t.Fatal(err)
		}
		return record.Inputs
	}
	pinned := readInputs(work)
	if len(pinned) != 3 || pinned[0].SourceTaskID != source.ID || len(pinned[0].NodeDigest) != 64 || pinned[1].SourceTaskID != 0 || len(pinned[1].NodeDigest) != 64 {
		t.Fatalf("input provenance missing: %+v", pinned)
	}
	knowledge, err := s.TeamKnowledgeForIntent(t.Context(), work, nil, 12)
	if err != nil {
		t.Fatal(err)
	}
	sources := map[int64]bool{}
	for _, item := range knowledge {
		if item.Prerequisite {
			sources[item.Node.ID] = true
		}
	}
	if !sources[finding] || !sources[local] || sources[foreign] {
		t.Fatalf("missing source handoff or leaked unrelated node: %+v", sources)
	}
	for _, input := range []WorkInput{
		{Kind: "poc", NodeID: foreign, Purpose: "unrelated task"},
		{Kind: "result", NodeID: local, Purpose: "not a result"},
		{Kind: "finding", NodeID: local, Purpose: "not a finding"},
		{Kind: "poc", Locator: "/poc", Purpose: "missing version"},
		{Kind: "poc", NodeID: finding, NodeDigest: "stale", Purpose: "wrong version"},
		{Kind: "poc", NodeID: finding, SourceTaskID: unrelated.ID, Purpose: "forged source"},
		{Kind: "unknown", Locator: "/poc", Revision: "v1", Purpose: "unknown kind"},
	} {
		if _, err := s.AddWorkWithInputs("bad input", "analyst", "inspect", "planner", nil, nil, []WorkInput{input}); err == nil {
			t.Fatalf("accepted invalid input: %+v", input)
		}
	}
	if _, err := s.AddWorkWithInputs("duplicate", "analyst", "inspect", "planner", nil, nil, []WorkInput{inputs[0], inputs[0]}); err == nil {
		t.Fatal("accepted duplicate inputs")
	}
	if ok, err := s.ClaimIntent(work, "author"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	result := func(kind string) (int64, error) {
		return s.RecordWorkFact(map[string]any{"record_type": "result", "result_type": kind, "producer_intent_id": work,
			"summary": "adapted implementation", "confidence": "observed", "evidence": "controlled fixture", "inputs": []WorkInput{{Kind: "reference", Purpose: "forged"}}}, work, "author")
	}
	resultID, err := result("implementation")
	if err != nil {
		t.Fatal(err)
	}
	if got := readInputs(resultID); len(got) != 3 || got[0] != pinned[0] || got[2] != pinned[2] {
		t.Fatalf("producer omitted or replaced input provenance: %+v", got)
	}
	if _, err = d.Exec(`UPDATE exploration_nodes SET payload=payload || '{"summary":"revised observation"}'::jsonb WHERE id=$1`, finding); err != nil {
		t.Fatal(err)
	}
	if _, err = result("implementation"); err == nil {
		t.Fatal("accepted result after source drift")
	}
	blocker, err := result("blocker")
	if err != nil || readInputs(blocker)[0] != pinned[0] {
		t.Fatal("could not preserve blocker and original provenance", err)
	}
	// Replanning can pin the new source, but removing the relation cannot
	// preserve access merely because a stale ID remains in a work payload.
	next, err := s.AddWorkWithInputs("retry", "implementer", "check", "planner", nil, nil, inputs[:1])
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ClaimIntent(next, "retry"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err = d.Exec(`DELETE FROM task_relations WHERE task_id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordWorkFact(map[string]any{"record_type": "result", "result_type": "analysis"}, next, "retry"); err == nil {
		t.Fatal("accepted source after relation removal")
	}
}
