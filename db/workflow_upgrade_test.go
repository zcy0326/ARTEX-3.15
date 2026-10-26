package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

// CI supplies the exact schema from the deployed PR #1 merge, rather than
// emulating an upgrade by creating a fresh database with today's schema.
func TestGeneralUpgradeFromDeployedPR1(t *testing.T) {
	dsn := testDSN(t)
	path := os.Getenv("ARTEX_UPGRADE_SCHEMA_PATH")
	if path == "" {
		t.Skip("set ARTEX_UPGRADE_SCHEMA_PATH to main@a375584:db/schema.sql")
	}
	legacySchema, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(legacySchema)); got != "17f0f9e28e1a587f48e8723dfc84b5daf25887642e54b12801f95f1fcaea4002" {
		t.Fatal("upgrade fixture must be the exact schema from deployed PR #1")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := fmt.Sprintf("artex_upgrade_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("upgrade regression requires a PostgreSQL URL")
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	isolatedDSN := u.String()
	legacy, err := sql.Open("pgx", isolatedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if _, err = legacy.Exec(string(legacySchema)); err != nil {
		t.Fatalf("create deployed schema: %v", err)
	}
	// These seed functions have not changed since PR #1. Run them before the
	// user's customizations to model an already initialized installation.
	if err = (&DB{legacy}).seedBuiltins(); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec(`
INSERT INTO llm_profiles(name,format,base_url,model,api_key,max_tokens)
 VALUES('upgrade-user-model','openai','https://example.invalid','fixture-model','fixture-only',2048);
INSERT INTO explorations(description,goal) VALUES('existing security task','retain existing evidence'),('related task','retain relation');
INSERT INTO tasks(name,description,goal,exploration_id,status,paused,coverage_enabled,llm_profile_id,active_llm_profile_id)
 VALUES('existing task','existing security task','retain existing evidence',1,'paused',true,false,1,1),
 ('source task','related task','retain relation',2,'done',false,true,NULL,NULL);
INSERT INTO task_relations(task_id,source_task_id) VALUES(1,2);
INSERT INTO task_llm_profiles(task_id,profile_id,position) VALUES(1,1,0);
INSERT INTO exploration_nodes(exploration_id,kind,payload,state,origin) VALUES
 (1,'goal','{"text":"retain evidence"}','open','user'),
 (1,'intent','{"summary":"existing security work"}','open','planner'),
 (1,'fact','{"summary":"existing observation","confidence":"observed","evidence":"fixture output"}','confirmed','worker'),
 (1,'finding','{"summary":"existing finding","confidence":"observed"}','confirmed','worker');
INSERT INTO exploration_edges(exploration_id,src_id,dst_id,rel) VALUES(1,1,2,'derived_from'),(1,2,3,'yields'),(1,2,4,'yields');
INSERT INTO findings(task_id,node_id,name,vulnclass,severity,summary,evidence,status,report)
 VALUES(1,4,'saved finding','fixture','low','saved summary','saved evidence','confirmed','# Saved report');
INSERT INTO activity(exploration_id,node_id,worker,kind,summary,detail,metadata)
 VALUES(1,2,'worker','tool_result','saved execution','saved output','{"fixture":true}');
INSERT INTO conversations(agent_key,title,llm_profile_id) VALUES('worker','saved conversation',1);
INSERT INTO conversation_activities(conversation_id,worker,kind,summary,detail)
 VALUES(1,'worker','text','saved message','saved content');
INSERT INTO task_templates(name,nkey,description,goal,intercept_rules)
 VALUES('User security template','user security template','saved description','saved goal','[]');
INSERT INTO tools(key,system,description,schema,agents,enabled)
 VALUES('node_detail',true,'user tool description','{"type":"object","properties":{"id":{"type":"integer","description":"user parameter"}}}','["worker"]',false),
 ('upgrade_custom_tool',false,'user command','{"type":"object"}','["worker"]',true);
INSERT INTO settings(key,value) VALUES('upgrade_user_setting','saved value');
UPDATE agents SET enabled=false,interactive_shell=false,max_turns=17,run_seconds=333,llm_profile_id=1 WHERE key='worker';
INSERT INTO agent_prompts(agent_id,version,template_text,note)
 SELECT id,1,'user worker prompt','saved customization' FROM agents WHERE key='worker';
UPDATE agents SET current_prompt_id=(SELECT id FROM agent_prompts WHERE template_text='user worker prompt') WHERE key='worker';
INSERT INTO agent_skill_visibility(agent_id,skill_name,enabled) SELECT id,'user-skill',false FROM agents WHERE key='worker';
INSERT INTO mcp_servers(name,transport,url,enabled) VALUES('user-mcp','sse','https://example.invalid/mcp',false);
`); err != nil {
		t.Fatalf("populate deployed installation: %v", err)
	}
	var newColumns int
	if err = legacy.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=$1
AND table_name IN ('tasks','task_templates') AND column_name IN ('workflow','acceptance')`, schema).Scan(&newColumns); err != nil || newColumns != 0 {
		t.Fatal("fixture unexpectedly contains the new workflow columns", err)
	}
	// Compare full original rows, including IDs, evidence and configuration. Only
	// ordinary startup timestamps and the intentionally added columns are ignored.
	tables := []string{"tasks", "task_templates", "explorations", "exploration_nodes", "exploration_edges",
		"findings", "activity", "conversations", "conversation_activities", "task_relations", "task_llm_profiles",
		"agents", "agent_prompts", "llm_profiles", "settings", "tools", "mcp_servers", "agent_skill_visibility"}
	snapshot := func(conn *sql.DB, table string) string {
		t.Helper()
		var value string
		err := conn.QueryRow(`SELECT COALESCE(jsonb_agg(value ORDER BY value::text),'[]')::text FROM
 (SELECT to_jsonb(saved)-'updated_at'-'workflow'-'acceptance' AS value FROM ` + table + ` saved) records`).Scan(&value)
		if err != nil {
			t.Fatal(table, err)
		}
		return value
	}
	before := map[string]string{}
	for _, table := range tables {
		before[table] = snapshot(legacy, table)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	for restart := range 2 {
		// Exercise the same Open path used by the upgraded application, twice.
		upgraded, err := Open(isolatedDSN)
		if err != nil {
			t.Fatalf("upgrade/restart %d: %v", restart, err)
		}
		t.Cleanup(func() { _ = upgraded.Close() })
		for _, table := range tables {
			if got := snapshot(upgraded.DB, table); got != before[table] {
				t.Fatalf("upgrade/restart %d changed original rows in %s", restart, table)
			}
		}
		task, err := upgraded.GetTask(1)
		if err != nil || task == nil || task.Workflow != WorkflowSecurity || task.Acceptance != "" || task.CoverageEnabled || !task.Paused {
			t.Fatalf("legacy task behavior changed: %+v %v", task, err)
		}
		template, err := upgraded.GetTaskTemplate(1)
		if err != nil || template == nil || template.Workflow != WorkflowSecurity || template.Acceptance != "" {
			t.Fatalf("legacy template behavior changed: %+v %v", template, err)
		}
		ready, err := upgraded.Exploration(task.ExplorationID).ReadyFrontier(20)
		if err != nil || len(ready) != 1 || ready[0].ID != 2 {
			t.Fatal("legacy work stopped being eligible", err)
		}
		if restart == 1 {
			general, err := upgraded.CreateTaskWithOptions("new general task", "new delivery", TaskCreateOptions{Workflow: WorkflowGeneral})
			if err != nil {
				t.Fatal("new tasks unusable after migration", err)
			}
			store := upgraded.Exploration(general.ExplorationID)
			first, err := store.AddWork("analyze", "analyst", "checked result", "planner", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.AddWork("adapt", "implementer", "checked change", "planner", []int64{first}, nil); err != nil {
				t.Fatal("old edge constraint did not upgrade", err)
			}
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
