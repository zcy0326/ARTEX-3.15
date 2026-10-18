package server

import (
	"log"

	"github.com/Autumn-27/artex/agent"
)

// Upgrade only the historical code-default bindings/descriptions once. Explicit
// disables and customized bindings remain authoritative; subsequent user edits
// are not overwritten at startup.
func (s *Server) seedTeamCollaboration() {
	const flag = "team_knowledge_handoffs_v1"
	if value, _, err := s.m.pg.GetSetting(flag); err != nil {
		log.Printf("[team] load migration flag: %v", err)
		return
	} else if value == "true" {
		return
	}
	if _, err := s.m.pg.Exec(`UPDATE tools SET agents=agents || '["worker"]'::jsonb
WHERE key='list_facts' AND system AND jsonb_array_length(agents)=2
AND agents @> '["mainagent","planner"]'::jsonb`); err != nil {
		log.Printf("[team] upgrade default fact reader: %v", err)
		return
	}
	const oldPrefix = "【通常不推荐使用，因为系统中已经给了大部分信息了】"
	for _, seed := range agent.BuiltinToolSeeds() {
		if seed.Key != "search_all_worker_traces" && seed.Key != "list_worker_traces" {
			continue
		}
		if _, err := s.m.pg.Exec(`UPDATE tools SET description=$2,updated_at=now()
WHERE key=$1 AND system AND description=$3`, seed.Key, seed.Desc, oldPrefix+seed.Desc); err != nil {
			log.Printf("[team] upgrade trace reader description: %v", err)
			return
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[team] save migration flag: %v", err)
	}
}
