package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const WorkflowSecurity = "security"
const WorkflowGeneral = "general"

// Empty remains the legacy security workflow, including on old API clients.
func NormalizeWorkflow(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", WorkflowSecurity:
		return WorkflowSecurity, nil
	case WorkflowGeneral:
		return WorkflowGeneral, nil
	default:
		return "", fmt.Errorf("workflow 必须为 security 或 general")
	}
}

func ValidWorkRole(role string) bool {
	switch role {
	case "analyst", "implementer", "verifier", "integrator":
		return true
	}
	return false
}

// AddWork creates an immutable dependency specification. Dependencies must
// already exist in this exploration, so new work cannot introduce a cycle.
// Lineage and execution prerequisites remain different edge relationships.
func (s *ExplorationStore) AddWork(summary, role, acceptance, origin string, dependencies, verifiedResults []int64) (int64, error) {
	return s.AddWorkWithInputs(summary, role, acceptance, origin, dependencies, verifiedResults, nil)
}

// AddWorkWithInputs pins the materials used for adaptation or implementation.
// Input provenance is separate from execution and validation prerequisites.
func (s *ExplorationStore) AddWorkWithInputs(summary, role, acceptance, origin string, dependencies, verifiedResults []int64, inputs []WorkInput) (int64, error) {
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(acceptance) == "" || !ValidWorkRole(role) {
		return 0, fmt.Errorf("summary、acceptance 必填；role 必须为 analyst/implementer/verifier/integrator")
	}
	if len(dependencies)+len(verifiedResults) > 32 {
		return 0, fmt.Errorf("工作前置最多 32 项")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	inputs, err = s.pinWorkInputs(tx, inputs)
	if err != nil {
		return 0, err
	}
	for _, group := range []struct {
		ids  []int64
		kind string
	}{{dependencies, KindIntent}, {verifiedResults, KindFact}} {
		seen := map[int64]bool{}
		for _, id := range group.ids {
			var kind, recordType string
			if id <= 0 || seen[id] {
				return 0, fmt.Errorf("前置 ID 无效或重复")
			}
			seen[id] = true
			err = tx.QueryRow(`SELECT kind, COALESCE(payload->>'record_type','') FROM exploration_nodes
WHERE id=$1 AND exploration_id=$2 AND state<>'deleted' FOR KEY SHARE`, id, s.expID).Scan(&kind, &recordType)
			if err != nil || kind != group.kind || (kind == KindFact && recordType != "result") {
				return 0, fmt.Errorf("前置 %d 必须是本任务的工作或成果，不能引用其他任务", id)
			}
		}
	}
	if dependencies == nil {
		dependencies = []int64{}
	}
	if verifiedResults == nil {
		verifiedResults = []int64{}
	}
	payload, _ := json.Marshal(map[string]any{"summary": summary, "work_role": role, "acceptance": acceptance,
		"depends_on": dependencies, "verified_result_ids": verifiedResults, "inputs": inputs})
	var id int64
	err = tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES($1,'intent',$2,5,'open',$3) RETURNING id`, s.expID, string(jsonbClean(payload)), origin).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, input := range inputs {
		if input.NodeID > 0 && input.SourceTaskID == 0 {
			if _, err = tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,dst_id,rel)
VALUES($1,$2,$3,'derived_from') ON CONFLICT DO NOTHING`, s.expID, input.NodeID, id); err != nil {
				return 0, err
			}
		}
	}
	for _, group := range []struct {
		ids []int64
		rel string
	}{{dependencies, "requires"}, {verifiedResults, "requires_validation"}} {
		for _, dep := range group.ids {
			if _, err = tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,dst_id,rel) VALUES($1,$2,$3,$4)`, s.expID, id, dep, group.rel); err != nil {
				return 0, err
			}
		}
	}
	_, err = tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,dst_id,rel)
SELECT $1,id,$2,'derived_from' FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='origin'
ON CONFLICT DO NOTHING`, s.expID, id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// Validation dependencies use the latest assessment for the exact immutable
// result digest. A later failed assessment revokes readiness. Counts also stop a
// removed prerequisite from silently turning into a satisfied prerequisite.
const workReadySQL = `
AND NOT EXISTS (SELECT 1 FROM exploration_edges e
  JOIN exploration_nodes p ON p.id=e.dst_id
  WHERE e.src_id=n.id AND e.rel='requires' AND (p.state<>'done' OR COALESCE((SELECT output.payload->>'result_type'
    FROM exploration_nodes output WHERE output.exploration_id=n.exploration_id AND output.kind='fact'
      AND output.payload->>'record_type'='result' AND output.payload->>'producer_intent_id'=p.id::text
    ORDER BY output.id DESC LIMIT 1),'')='blocker'))
AND NOT EXISTS (SELECT 1 FROM exploration_edges e
  JOIN exploration_nodes result ON result.id=e.dst_id
  WHERE e.src_id=n.id AND e.rel='requires_validation' AND COALESCE((
    SELECT v.payload->>'verdict' FROM exploration_nodes v
    WHERE v.exploration_id=n.exploration_id AND v.kind='fact' AND v.payload->>'record_type'='validation'
      AND v.payload->>'result_id'=result.id::text
      AND v.payload->>'result_digest'=result.payload->>'digest'
    ORDER BY v.id DESC LIMIT 1),'')<>'passed')
AND (NOT (n.payload ? 'work_role') OR (
  jsonb_array_length(n.payload->'depends_on')=(SELECT count(*) FROM exploration_edges WHERE src_id=n.id AND rel='requires')
  AND jsonb_array_length(n.payload->'verified_result_ids')=(SELECT count(*) FROM exploration_edges WHERE src_id=n.id AND rel='requires_validation')))
`

func (s *ExplorationStore) ReadyFrontier(limit int) ([]*Node, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes n
WHERE exploration_id=$1 AND kind='intent' AND state='open' `+workReadySQL+`
ORDER BY priority DESC,id ASC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// RecordWorkFact atomically persists a result/assessment and producer lineage.
func (s *ExplorationStore) RecordWorkFact(payload map[string]any, intentID int64, origin string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var kind string
	var workPayload []byte
	if intentID <= 0 {
		return 0, fmt.Errorf("成果必须由当前工作产生")
	}
	if err = tx.QueryRow(`SELECT kind,payload FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND state='running' FOR UPDATE`, intentID, s.expID).Scan(&kind, &workPayload); err != nil || kind != KindIntent {
		return 0, fmt.Errorf("当前工作不存在、已停止或不属于本任务")
	}
	var work struct {
		Inputs []WorkInput `json:"inputs"`
	}
	if err = json.Unmarshal(workPayload, &work); err != nil {
		return 0, err
	}
	// The producer cannot omit or replace the planned provenance. Changed or
	// inaccessible node inputs require replanning; blockers may still be saved.
	delete(payload, "inputs")
	if len(work.Inputs) > 0 {
		if payload["record_type"] == "result" && payload["result_type"] != "blocker" {
			if _, err = s.pinWorkInputs(tx, work.Inputs); err != nil {
				return 0, fmt.Errorf("输入已变化或不可访问，请记录 blocker 并重新规划：%w", err)
			}
		}
		payload["inputs"] = work.Inputs
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	if payload["record_type"] == "result" {
		var normalized map[string]any
		decoder := json.NewDecoder(bytes.NewReader(jsonbClean(raw)))
		decoder.UseNumber()
		if err = decoder.Decode(&normalized); err != nil {
			return 0, err
		}
		delete(normalized, "digest")
		canonical, _ := json.Marshal(normalized)
		sum := sha256.Sum256(canonical)
		normalized["digest"] = hex.EncodeToString(sum[:])
		payload["digest"] = normalized["digest"]
		raw, err = json.Marshal(normalized)
		if err != nil {
			return 0, err
		}
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, s.expID, string(jsonbClean(raw)), origin).Scan(&id)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,dst_id,rel) VALUES($1,$2,$3,'yields')`, s.expID, intentID, id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// WorkRecords uses the existing fact store so evidence survives inheritance,
// archival, restore and worker handoffs without a second source of truth.
func (s *ExplorationStore) WorkRecords(before int64, limit int) ([]*Node, bool, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact'
AND payload->>'record_type' IN ('result','validation') AND ($2=0 OR id<$2) ORDER BY id DESC LIMIT $3`, s.expID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	more := len(nodes) > limit
	if more {
		nodes = nodes[:limit]
	}
	if nodes == nil {
		nodes = []*Node{}
	}
	return nodes, more, err
}

// WorkResultVerdict returns the newest independent assessment for one version.
func (s *ExplorationStore) WorkResultVerdict(id int64, digest string) (string, error) {
	var verdict string
	err := s.db.QueryRow(`SELECT COALESCE((SELECT payload->>'verdict' FROM exploration_nodes
 WHERE exploration_id=$1 AND kind='fact' AND payload->>'record_type'='validation'
 AND payload->>'result_id'=$2::bigint::text AND payload->>'result_digest'=$3 ORDER BY id DESC LIMIT 1),'')`, s.expID, id, digest).Scan(&verdict)
	return verdict, err
}
