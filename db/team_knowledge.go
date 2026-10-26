package db

import (
	"context"
	"encoding/json"
)

// TeamKnowledge preserves the source of an observation. Node IDs are exploration
// handles, not standalone finding IDs; consumers can fetch node_detail for the
// complete record. Prerequisite means graph lineage, not a proven precondition.
type TeamKnowledge struct {
	Node         *Node
	FromIntentID int64
	Prerequisite bool
}

// TeamKnowledgeForIntent reads relevant peer observations without using an ID
// watermark: sequence allocation and transaction commit order can differ. Only
// explicitly delivered IDs are excluded, so a late commit remains discoverable.
// Related tasks are live, direct relations only, matching GetNodeWithSources.
func (s *ExplorationStore) TeamKnowledgeForIntent(ctx context.Context, intentID int64, seen []int64, limit int) ([]TeamKnowledge, error) {
	if limit <= 0 || limit > 24 {
		limit = 12
	}
	if seen == nil {
		seen = []int64{}
	}
	rawSeen, err := json.Marshal(seen)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
WITH visible AS (
    SELECT $1::bigint AS exploration_id, 0::bigint AS source_task_id
    UNION
    SELECT source.exploration_id, source.id
    FROM tasks owner
    JOIN task_relations relation ON relation.task_id=owner.id
    JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
    WHERE owner.exploration_id=$1 AND owner.deleted_at IS NULL
), parents AS (
    SELECT src_id FROM exploration_edges
    WHERE exploration_id=$1 AND dst_id=$2 AND rel='derived_from'
    UNION
    SELECT dst_id FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='requires_validation'
    UNION
    SELECT produced.dst_id FROM exploration_edges requirement
    JOIN exploration_edges produced ON produced.src_id=requirement.dst_id AND produced.rel='yields'
    WHERE requirement.exploration_id=$1 AND requirement.src_id=$2 AND requirement.rel='requires'
), targets AS (
    SELECT asset_id FROM exploration_anchors WHERE node_id=$2
)
SELECT n.id, n.kind, n.payload, n.priority, n.state, COALESCE(n.origin,''),
       COALESCE(n.owner,''), COALESCE(n.blocked_reason,''), COALESCE(n.delete_reason,''), n.created_at,
       visible.source_task_id,
       COALESCE((SELECT e.src_id FROM exploration_edges e
                 WHERE e.dst_id=n.id AND e.rel='yields' ORDER BY e.src_id LIMIT 1),0),
       EXISTS(SELECT 1 FROM parents WHERE src_id=n.id),
       COALESCE((SELECT jsonb_agg(a.asset_id ORDER BY a.asset_id)
                 FROM exploration_anchors a WHERE a.node_id=n.id),'[]'::jsonb)
FROM exploration_nodes n
JOIN visible ON visible.exploration_id=n.exploration_id
WHERE EXISTS(SELECT 1 FROM exploration_nodes local_intent
             WHERE local_intent.id=$2 AND local_intent.exploration_id=$1 AND local_intent.kind='intent')
  AND n.kind IN ('fact','finding') AND n.state<>'origin'
  AND n.id NOT IN (SELECT value::bigint FROM jsonb_array_elements_text($3::jsonb))
  AND NOT EXISTS(SELECT 1 FROM exploration_edges own_result
                 WHERE own_result.src_id=$2 AND own_result.dst_id=n.id AND own_result.rel='yields')
  AND (EXISTS(SELECT 1 FROM parents WHERE src_id=n.id)
       OR NOT EXISTS(SELECT 1 FROM targets)
       OR NOT EXISTS(SELECT 1 FROM exploration_anchors a WHERE a.node_id=n.id)
       OR EXISTS(SELECT 1 FROM exploration_anchors a JOIN targets ON targets.asset_id=a.asset_id WHERE a.node_id=n.id))
ORDER BY EXISTS(SELECT 1 FROM parents WHERE src_id=n.id) DESC, n.id DESC
LIMIT $4`, s.expID, intentID, string(rawSeen), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TeamKnowledge{}
	for rows.Next() {
		n := &Node{}
		item := TeamKnowledge{Node: n}
		var anchors []byte
		if err := rows.Scan(&n.ID, &n.Kind, &n.Payload, &n.Priority, &n.State,
			&n.Origin, &n.Owner, &n.BlockedReason, &n.DeleteReason, &n.CreatedAt,
			&n.SourceTaskID, &item.FromIntentID, &item.Prerequisite, &anchors); err != nil {
			return nil, err
		}
		n.Inherited = n.SourceTaskID > 0
		if err := json.Unmarshal(anchors, &n.Anchors); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
