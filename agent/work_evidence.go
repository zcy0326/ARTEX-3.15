package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/workproduct"
)

// General goal completion rejects self-reported implementation success and
// inconclusive/failed assessments. Security goal semantics remain unchanged.
func tsxGeneralEvidence(t *ToolSet, node *db.Node) error {
	var p struct {
		RecordType                            string `json:"record_type"`
		ResultType                            string `json:"result_type"`
		Confidence, Evidence, Verdict, Digest string
		ResultID                              int64  `json:"result_id"`
		ResultDigest                          string `json:"result_digest"`
		Files                                 []workproduct.Artifact
	}
	if err := json.Unmarshal(node.Payload, &p); err != nil {
		return err
	}
	if p.RecordType == "validation" {
		if p.Verdict != "passed" {
			return fmt.Errorf("验证未通过，不能据此完成目标")
		}
		result, err := t.ts.GetNode(p.ResultID)
		if err != nil || result == nil {
			return fmt.Errorf("验证引用的本任务成果已不存在")
		}
		var payload struct{ Digest string }
		_ = json.Unmarshal(result.Payload, &payload)
		if payload.Digest != p.ResultDigest {
			return fmt.Errorf("验证不对应当前成果版本")
		}
		verdict, err := t.ts.WorkResultVerdict(p.ResultID, p.ResultDigest)
		if err != nil || verdict != "passed" {
			return fmt.Errorf("该成果最新验证未通过")
		}
		return tsxGeneralEvidence(t, result)
	}
	if p.ResultType == "blocker" {
		return fmt.Errorf("阻塞记录不能证明目标完成")
	}
	verdict := ""
	if p.RecordType == "result" {
		var err error
		verdict, err = t.ts.WorkResultVerdict(node.ID, p.Digest)
		if err != nil {
			return err
		}
		if verdict != "" && verdict != "passed" {
			return fmt.Errorf("成果的最新验证未通过")
		}
	}
	if (p.Confidence != "observed" && verdict != "passed") || p.Evidence == "" {
		return fmt.Errorf("需要实际观察与证据；推断不能直接完成目标")
	}
	if p.RecordType == "result" && p.ResultType == "implementation" {
		if verdict != "passed" {
			return fmt.Errorf("实现成果需要精确版本的最新独立验证通过")
		}
	}
	for _, file := range p.Files {
		f, err := workproduct.OpenVerified(t.workspaceRoot, file)
		if err != nil {
			return err
		}
		f.Close()
	}
	return nil
}
