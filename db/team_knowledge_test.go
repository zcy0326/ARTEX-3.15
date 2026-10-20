package db

import (
	"fmt"
	"testing"
	"time"
)

func TestTeamKnowledgeScopesRelevanceAndDeliveredIDs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	makeTask := func(description string, sources []int64) *Task {
		t.Helper()
		task, err := d.CreateTaskWithOptions(description, "fixture", TaskCreateOptions{SourceTaskIDs: sources})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
		return task
	}
	grand := makeTask("grandparent", nil)
	source := makeTask("direct source", []int64{grand.ID})
	current := makeTask("current", []int64{source.ID})
	stranger := makeTask("unrelated", nil)
	store := d.Exploration(current.ExplorationID)
	var assets []int64
	for i := range 2 {
		id, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("team-%d-%d.example.test", time.Now().UnixNano(), i)})
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, id)
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=$1`, id) })
	}
	intent, err := store.AddIntent(map[string]any{"summary": "fixture"}, 5, assets[:1], "planner")
	if err != nil {
		t.Fatal(err)
	}
	add := func(task *Task, summary string, anchors []int64) int64 {
		t.Helper()
		id, err := d.Exploration(task.ExplorationID).AddNode(KindFact, map[string]any{"summary": summary, "detail": "full evidence", "confidence": "inferred"}, 5, "confirmed", "worker", anchors)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	related := add(current, "same asset", assets[:1])
	unrelatedAsset := add(current, "different asset", assets[1:])
	parent := add(source, "explicit parent on another asset", assets[1:])
	if err := store.Link(parent, RelDerivedFrom, intent); err != nil {
		t.Fatal(err)
	}
	global := add(current, "task-wide observation", nil)
	own := add(current, "own output", assets[:1])
	if err := store.Link(intent, RelYields, own); err != nil {
		t.Fatal(err)
	}
	grandID := add(grand, "not inherited transitively", nil)
	strangerID := add(stranger, "other task secret", nil)
	inherited := add(source, "direct source", assets[:1])
	items, err := store.TeamKnowledgeForIntent(t.Context(), intent, nil, 24)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]TeamKnowledge{}
	for _, item := range items {
		byID[item.Node.ID] = item
	}
	for _, id := range []int64{related, global, parent, inherited} {
		if byID[id].Node == nil {
			t.Errorf("missing relevant observation %d", id)
		}
	}
	for _, id := range []int64{unrelatedAsset, own, grandID, strangerID} {
		if byID[id].Node != nil {
			t.Errorf("unexpected observation %d", id)
		}
	}
	if len(items) == 0 || items[0].Node.ID != parent || !items[0].Prerequisite || items[0].Node.SourceTaskID != source.ID {
		t.Fatalf("parent priority or provenance lost: %+v", items)
	}
	// Excluding a higher delivered ID must not hide an older observation that
	// committed later. No id>watermark filter may be used here.
	items, err = store.TeamKnowledgeForIntent(t.Context(), intent, []int64{inherited}, 24)
	if err != nil {
		t.Fatal(err)
	}
	foundRelated := false
	for _, item := range items {
		if item.Node.ID == inherited {
			t.Fatal("delivered observation repeated")
		}
		foundRelated = foundRelated || item.Node.ID == related
	}
	if !foundRelated {
		t.Fatal("older undelivered observation hidden")
	}
	foreignIntent, err := d.Exploration(stranger.ExplorationID).AddIntent(map[string]any{"summary": "foreign"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if leaked, err := store.TeamKnowledgeForIntent(t.Context(), foreignIntent, nil, 24); err != nil || len(leaked) != 0 {
		t.Fatalf("foreign intent accepted: %+v %v", leaked, err)
	}
}
