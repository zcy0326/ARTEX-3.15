package db

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// WorkInput records what existing material a work item will reuse and why.
// Node provenance is resolved by the server. External revisions are declarations
// that the worker must inspect, not an attestation that a file was verified.
type WorkInput struct {
	Kind         string `json:"kind"`
	Purpose      string `json:"purpose"`
	Locator      string `json:"locator,omitempty"`
	Revision     string `json:"revision,omitempty"`
	NodeID       int64  `json:"node_id,omitempty"`
	NodeDigest   string `json:"node_digest,omitempty"`
	SourceTaskID int64  `json:"source_task_id,omitempty"`
}

func workInputDigest(kind, state string, payload []byte) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal([]any{kind, state, value})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func (s *ExplorationStore) pinWorkInputs(tx *sql.Tx, inputs []WorkInput) ([]WorkInput, error) {
	if len(inputs) > 16 {
		return nil, fmt.Errorf("每项工作最多 16 项输入")
	}
	out := make([]WorkInput, 0, len(inputs))
	seen := map[string]bool{}
	for _, input := range inputs {
		input.Kind = strings.TrimSpace(utf8Clean(input.Kind))
		input.Purpose = strings.TrimSpace(utf8Clean(input.Purpose))
		input.Locator = strings.TrimSpace(utf8Clean(input.Locator))
		input.Revision = strings.TrimSpace(utf8Clean(input.Revision))
		switch input.Kind {
		case "repository", "poc", "reverse_analysis", "result", "finding", "reference":
		default:
			return nil, fmt.Errorf("输入 kind 无效")
		}
		if input.Purpose == "" || len(input.Purpose) > 4000 || len(input.Locator) > 4000 || len(input.Revision) > 1024 || input.NodeID < 0 {
			return nil, fmt.Errorf("输入用途必填，且输入说明与位置须受限")
		}
		key := fmt.Sprintf("node:%d", input.NodeID)
		if input.NodeID > 0 {
			var kind, state string
			var payload []byte
			var sourceID int64
			// Direct source tasks remain read-only. No transitive or unrelated
			// task can be made visible by supplying an ID in a work payload.
			err := tx.QueryRow(`SELECT n.kind,n.state,n.payload,CASE WHEN n.exploration_id=$2 THEN 0 ELSE
 (SELECT id FROM tasks WHERE exploration_id=n.exploration_id AND deleted_at IS NULL) END
FROM exploration_nodes n WHERE n.id=$1 AND n.kind IN ('fact','finding') AND n.state<>'deleted'
AND (n.exploration_id=$2 OR EXISTS (
 SELECT 1 FROM tasks owner JOIN task_relations r ON r.task_id=owner.id
 JOIN tasks source ON source.id=r.source_task_id AND source.deleted_at IS NULL
 WHERE owner.exploration_id=$2 AND owner.deleted_at IS NULL AND source.exploration_id=n.exploration_id))
FOR SHARE OF n`, input.NodeID, s.expID).Scan(&kind, &state, &payload, &sourceID)
			if err != nil {
				return nil, fmt.Errorf("输入节点 %d 必须是本任务或直接关联任务的可读事实/漏洞", input.NodeID)
			}
			var record struct {
				RecordType string `json:"record_type"`
			}
			_ = json.Unmarshal(payload, &record)
			if (input.Kind == "finding" && kind != KindFinding) || (input.Kind == "result" && (kind != KindFact || record.RecordType != "result")) {
				return nil, fmt.Errorf("输入节点 %d 与 kind 不匹配", input.NodeID)
			}
			digest, err := workInputDigest(kind, state, payload)
			if err != nil {
				return nil, err
			}
			if (input.NodeDigest != "" && input.NodeDigest != digest) || (input.SourceTaskID != 0 && input.SourceTaskID != sourceID) {
				return nil, fmt.Errorf("输入节点 %d 的版本或来源不匹配", input.NodeID)
			}
			input.NodeDigest, input.SourceTaskID = digest, sourceID
		} else {
			if input.Locator == "" || input.Revision == "" || input.NodeDigest != "" || input.SourceTaskID != 0 || input.Kind == "result" || input.Kind == "finding" {
				return nil, fmt.Errorf("外部输入须提供 locator 和 revision；result/finding 须引用 node_id")
			}
			key = "external:" + input.Kind + ":" + input.Locator + ":" + input.Revision
		}
		if seen[key] {
			return nil, fmt.Errorf("输入重复，请合并用途说明")
		}
		seen[key] = true
		out = append(out, input)
	}
	return out, nil
}
