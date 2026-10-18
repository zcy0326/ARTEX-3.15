package server

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestTeamFactNotificationsCoalesceWithoutLosingOtherTriggers(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	task.NotifyFinding(3, "finding")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { task.NotifyFact(7, "new fact") })
	}
	wg.Wait()
	task.NotifyFact(8, "different worker")
	events := task.drainTriggers()
	if len(events) != 3 || events[0].Kind != "finding" || events[1].Kind != "fact" || events[2].IntentID != 8 {
		t.Fatalf("unexpected events: %+v", events)
	}
	if strings.Count(events[1].Detail, "new fact") != 20 {
		t.Fatal("fact burst lost before debounce")
	}
	select {
	case <-task.notify:
	default:
		t.Fatal("planner not woken")
	}
	if len(task.drainTriggers()) != 0 {
		t.Fatal("events delivered twice")
	}
}

func TestTeamMigrationPreservesDisableAndSubsequentUnbinding(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, seed := range agent.BuiltinToolSeeds() {
		if seed.Key == "list_facts" {
			schema, _ := json.Marshal(seed.Schema)
			bindings, _ := json.Marshal(seed.Agents)
			if err := pg.SeedTool(seed.Key, seed.Desc, schema, bindings); err != nil {
				t.Fatal(err)
			}
		}
	}
	const flag = "team_knowledge_handoffs_v1"
	oldFlag, existed, err := pg.GetSetting(flag)
	if err != nil {
		t.Fatal(err)
	}
	old, err := pg.GetTool("list_facts")
	if err != nil || old == nil {
		t.Fatal("missing fact reader", err)
	}
	t.Cleanup(func() {
		bindings, _ := json.Marshal(old.Agents)
		_, _ = pg.Exec(`UPDATE tools SET agents=$1,enabled=$2,description=$3 WHERE key='list_facts'`, string(bindings), old.Enabled, old.Description)
		if existed {
			_ = pg.SetSetting(flag, oldFlag)
		} else {
			_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, flag)
		}
	})
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, flag); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(`UPDATE tools SET agents='["mainagent","planner"]'::jsonb,enabled=false,description='custom reader' WHERE key='list_facts'`); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: &Manager{pg: pg}}
	s.seedTeamCollaboration()
	row, _ := pg.GetTool("list_facts")
	if row == nil || row.Enabled || row.Description != "custom reader" || !slices.Contains(row.Agents, "worker") {
		t.Fatalf("migration changed customization or lost new default: %+v", row)
	}
	if err := pg.RemoveAgentFromTool("worker", "list_facts"); err != nil {
		t.Fatal(err)
	}
	s.seedTeamCollaboration()
	row, _ = pg.GetTool("list_facts")
	if slices.Contains(row.Agents, "worker") {
		t.Fatal("restart overwrote user's subsequent unbinding")
	}
	// An explicitly customized binding is preserved on an upgrade as well.
	_, _ = pg.Exec(`DELETE FROM settings WHERE key=$1`, flag)
	bindings, _ := json.Marshal([]string{"mainagent"})
	_, _ = pg.Exec(`UPDATE tools SET agents=$1 WHERE key='list_facts'`, string(bindings))
	s.seedTeamCollaboration()
	row, _ = pg.GetTool("list_facts")
	if slices.Contains(row.Agents, "worker") {
		t.Fatal("custom binding overwritten")
	}
}
