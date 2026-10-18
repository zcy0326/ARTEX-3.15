package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

func teamObservation(id int64, detail string) db.TeamKnowledge {
	payload, _ := json.Marshal(map[string]any{"summary": "peer observation", "detail": detail, "confidence": "inferred"})
	return db.TeamKnowledge{Node: &db.Node{ID: id, Kind: db.KindFact, Payload: payload, Origin: "worker-2"}, FromIntentID: 42}
}

func TestTeamContextPreservesEvidenceAndProvenance(t *testing.T) {
	item := teamObservation(9, "具体条件与完整步骤")
	item.Prerequisite = true
	item.Node.Inherited, item.Node.SourceTaskID = true, 77
	var payload map[string]any
	_ = json.Unmarshal(item.Node.Payload, &payload)
	payload["evidence"] = map[string]any{"poc": strings.Repeat("证据😀", 3000)}
	item.Node.Payload, _ = json.Marshal(payload)
	note, ids := renderTeamKnowledge([]db.TeamKnowledge{item}, true)
	if !utf8.ValidString(note) || len(ids) != 1 || ids[0] != 9 {
		t.Fatalf("invalid delivery: %v %q", ids, note)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(note[strings.Index(note, "[{"):]), &rows); err != nil {
		t.Fatal(err)
	}
	row := rows[0]
	if row["confidence"] != "inferred" || row["source_task_id"] != float64(77) || row["from_intent"] != float64(42) || row["prerequisite_source"] != true {
		t.Fatalf("lost provenance: %+v", row)
	}
	if row["detail"] != "具体条件与完整步骤" || row["truncated"] != true || row["detail_tool"] != "node_detail(id=9)" {
		t.Fatalf("lost details or restore handle: %+v", row)
	}
}

func TestTeamFeedRetryThrottleAndLateCommit(t *testing.T) {
	now := time.Unix(100, 0)
	calls := 0
	f := &teamFeed{seen: map[int64]bool{}, now: func() time.Time { return now }}
	f.load = func(_ context.Context, seen []int64, _ int) ([]db.TeamKnowledge, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary read failure")
		}
		if calls == 2 {
			return []db.TeamKnowledge{teamObservation(100, "committed first")}, nil
		}
		// A smaller sequence value can become visible after a larger one.
		return []db.TeamKnowledge{teamObservation(99, "late commit"), teamObservation(100, "duplicate")}, nil
	}
	if note := f.poll(t.Context(), true); note != "" || len(f.seen) != 0 {
		t.Fatal("failed read consumed observations")
	}
	if note := f.poll(t.Context(), false); note != "" || calls != 1 {
		t.Fatal("poll not throttled")
	}
	now = now.Add(11 * time.Second)
	if note := f.poll(t.Context(), false); !strings.Contains(note, "committed first") {
		t.Fatal(note)
	}
	now = now.Add(11 * time.Second)
	if note := f.poll(t.Context(), false); !strings.Contains(note, "late commit") || strings.Contains(note, "duplicate") {
		t.Fatal(note)
	}
}

func TestTeamFeedDoesNotConsumeBudgetOmissions(t *testing.T) {
	var items []db.TeamKnowledge
	for id := int64(1); id <= 12; id++ {
		item := teamObservation(id, strings.Repeat("detail", 1000))
		item.Prerequisite = true
		items = append(items, item)
	}
	f := &teamFeed{seen: map[int64]bool{}, now: time.Now, load: func(context.Context, []int64, int) ([]db.TeamKnowledge, error) { return items, nil }}
	f.poll(t.Context(), true)
	first := len(f.seen)
	if first == 0 || first == len(items) {
		t.Fatalf("expected partial bounded delivery, got %d", first)
	}
	f.poll(t.Context(), true)
	if len(f.seen) <= first {
		t.Fatal("omitted observations were not retried")
	}
}

func TestTeamToolPreservesResultAndConcurrentDelivery(t *testing.T) {
	var calls atomic.Int32
	var deliveries atomic.Int32
	base := actool.Build(actool.Spec{Name: "fixture", RawInput: true, Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		calls.Add(1)
		return actool.Result{Content: []llm.ContentBlock{llm.TextBlock("original error result")}, IsError: true,
			Extra: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.TextBlock("existing extra")}}}}, nil
	}})
	f := &teamFeed{seen: map[int64]bool{}, now: time.Now, load: func(context.Context, []int64, int) ([]db.TeamKnowledge, error) {
		return []db.TeamKnowledge{teamObservation(1, "new context")}, nil
	}}
	tool := teamAwareTool{CoreTool: base, feed: f}
	if !tool.AcceptsRawInput() {
		t.Fatal("raw-input contract lost")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			result, err := tool.Call(t.Context(), nil, nil)
			if err != nil || !result.IsError || result.Flatten() != "original error result" || result.Extra[0].Text() != "existing extra" {
				t.Error("underlying result changed")
			}
			if len(result.Extra) == 2 {
				deliveries.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 16 || deliveries.Load() != 1 {
		t.Fatalf("calls=%d deliveries=%d", calls.Load(), deliveries.Load())
	}
}

func TestTeamToolFailureAndCancellationDoNotConsumeUpdates(t *testing.T) {
	reads := 0
	f := &teamFeed{seen: map[int64]bool{}, now: time.Now, load: func(context.Context, []int64, int) ([]db.TeamKnowledge, error) {
		reads++
		return []db.TeamKnowledge{teamObservation(1, "update")}, nil
	}}
	base := actool.Build(actool.Spec{Name: "fail", Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
		return actool.Result{}, errors.New("tool failed")
	}})
	_, err := (teamAwareTool{CoreTool: base, feed: f}).Call(t.Context(), nil, nil)
	if err == nil || reads != 0 {
		t.Fatal("failed tool consumed updates")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if f.poll(ctx, true) != "" || reads != 0 {
		t.Fatal("cancelled run read updates")
	}
}

func TestFactNoticeIncludesOnlyCommittedItems(t *testing.T) {
	notice := factNotice([]factItem{{Summary: "one"}, {Summary: "failed"}, {Summary: "three"}}, []int64{1, 0, 3})
	if notice != "#1 one；#3 three" {
		t.Fatal(notice)
	}
}

func TestTeamWritesUseExecutionIdentityAndNotifyCommittedFacts(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("team write identity", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "own work"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.AddIntent(map[string]any{"summary": "peer work"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	tools := NewToolSet(store, "worker-1")
	tools.SetTaskID(task.ID)
	tools.runIntentID = intent
	var notices int
	tools.SetNotifyFact(func(id int64, summary string) {
		notices++
		if id != intent || !strings.Contains(summary, "committed") || strings.Contains(summary, "bad") {
			t.Errorf("incorrect committed notice: %d %s", id, summary)
		}
		// The callback runs after persistence, not before it.
		facts, err := store.ListByKind(db.KindFact, 20)
		if err != nil || len(facts) < 2 {
			t.Error("notice fired before fact commit")
		}
	})
	res, err := tools.recordFact().Call(t.Context(), json.RawMessage(`{"facts":[{"summary":"committed"},{"summary":"bad","intent_id":`+jsonNumber(other)+`}]}`), nil)
	if err != nil || res.IsError || notices != 1 {
		t.Fatalf("mixed batch: %s %v notices=%d", res.Flatten(), err, notices)
	}
	if _, err := tools.recordOneFact(factItem{Summary: "bad", IntentID: json.RawMessage(jsonNumber(other))}, 0); err == nil {
		t.Fatal("worker attributed fact to a peer")
	}
	report, err := tools.addFinding().Call(t.Context(), json.RawMessage(`{"vulnclass":"fixture","summary":"verified local fixture","severity":"low","evidence":"fixture output"}`), nil)
	if err != nil || report.IsError {
		t.Fatalf("finding with omitted intent: %v %s", err, report.Flatten())
	}
	rejected, err := tools.addFinding().Call(t.Context(), json.RawMessage(`{"vulnclass":"fixture","summary":"wrong owner","severity":"low","intent_id":`+jsonNumber(other)+`}`), nil)
	if err != nil || !rejected.IsError {
		t.Fatal("worker attributed finding to a peer")
	}
	yields := 0
	for _, edge := range mustTeamEdges(t, store) {
		if edge.Rel == db.RelYields && edge.From == intent {
			yields++
		}
	}
	if yields != 2 {
		t.Fatalf("omitted intent_id lost execution lineage: yields=%d", yields)
	}
}

func TestTeamDefaultReadersDoNotGrantDispatchTools(t *testing.T) {
	bound := map[string]bool{}
	for _, tool := range NewToolSet(nil, "").WorkerTools() {
		bound[tool.Name()] = true
	}
	if !bound["list_facts"] || !bound["list_worker_traces"] || !bound["node_detail"] {
		t.Fatal("peer readers missing")
	}
	for _, forbidden := range []string{"add_intent", "set_goals", "add_task_scope", "add_company_scope"} {
		if bound[forbidden] {
			t.Fatalf("collaboration granted %s", forbidden)
		}
	}
}

func jsonNumber(id int64) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func mustTeamEdges(t *testing.T, store *db.ExplorationStore) []db.Edge {
	t.Helper()
	edges, err := store.Edges(100)
	if err != nil {
		t.Fatal(err)
	}
	return edges
}
