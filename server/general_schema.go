package server

import (
	"encoding/json"
	"log"

	"github.com/Autumn-27/artex/agent"
)

// Add new spawn parameters without resetting descriptions, defaults, bindings
// or disabled tools. Existing properties win over the code's added properties.
func (s *Server) refreshGeneralSpawnSchema() {
	_, err := s.m.pg.Exec(`UPDATE tools SET schema=jsonb_set(schema,'{properties}',
  '{"workflow":{"type":"string","enum":["security","general"],"description":"任务类型；缺省继承父任务，否则 security"},
    "acceptance":{"type":"string","description":"可核验的交付条件"}}'::jsonb || COALESCE(schema->'properties','{}'::jsonb)),updated_at=now()
WHERE key='spawn_task' AND system AND (NOT COALESCE(schema->'properties','{}'::jsonb) ? 'workflow'
  OR NOT COALESCE(schema->'properties','{}'::jsonb) ? 'acceptance')`)
	if err != nil {
		log.Printf("[workflow] spawn schema migration: %v", err)
	}
	// Optional reuse parameters are additive, including on installations which
	// already used the first general workflow schema. Existing properties win.
	for _, seed := range agent.BuiltinToolSeeds() {
		property := map[string]string{"add_work": "inputs", "prepare_worktree": "base_commit"}[seed.Key]
		if property == "" {
			continue
		}
		properties, _ := seed.Schema["properties"].(map[string]any)
		added, err := json.Marshal(map[string]any{property: properties[property]})
		if err != nil {
			log.Printf("[workflow] %s schema: %v", seed.Key, err)
			continue
		}
		_, err = s.m.pg.Exec(`UPDATE tools SET schema=jsonb_set(schema,'{properties}',
 $2::jsonb || COALESCE(schema->'properties','{}'::jsonb)),updated_at=now()
 WHERE key=$1 AND system AND NOT COALESCE(schema->'properties','{}'::jsonb) ? $3`, seed.Key, string(added), property)
		if err != nil {
			log.Printf("[workflow] %s schema migration: %v", seed.Key, err)
		}
	}
}
