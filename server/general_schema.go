package server

import "log"

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
}
