package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/workproduct"
	actool "github.com/Autumn-27/norma/tool"
)

func stringEnum(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}
func idArray(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": description}
}

func (t *ToolSet) addWork() actool.CoreTool {
	return t.writeExpTool("add_work", "创建具有职责、验收和正式前置的通用工作。depends_on 等待已有工作正常结束；verified_result_ids 等待成果的最新独立验证通过。先等成果真实产生再引用其 ID。",
		obj(map[string]any{"summary": str("具体工作与输入"), "role": stringEnum("执行职责", "analyst", "implementer", "verifier", "integrator"),
			"acceptance": str("怎样验收这项工作"), "depends_on": idArray("必须正常结束的本任务工作 ID；不代表验收通过"),
			"verified_result_ids": idArray("必须取得独立 passed 验证的本任务成果 ID")}, "summary", "role", "acceptance"),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var input struct {
				Summary, Role, Acceptance string
				DependsOn                 []int64 `json:"depends_on"`
				Results                   []int64 `json:"verified_result_ids"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return actool.Errorf("无效 JSON"), nil
			}
			if len(input.Summary) > 4000 || len(input.Acceptance) > 16000 {
				return actool.Errorf("工作说明过长"), nil
			}
			id, err := t.ts.AddWork(input.Summary, input.Role, input.Acceptance, t.worker, input.DependsOn, input.Results)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if t.notify != nil {
				t.notify()
			}
			return jsonResult(map[string]any{"work_id": id})
		})
}

func (t *ToolSet) listWork() actool.CoreTool {
	return t.readExpTool("list_work", "查看本任务工作、职责、验收、前置及状态；最多 200 项。用 node_detail 查看完整工作。",
		obj(map[string]any{}), func(context.Context, json.RawMessage) (actool.Result, error) {
			nodes, err := t.ts.ListByKind(db.KindIntent, 200)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(nodes)
		})
}

func (t *ToolSet) listResults() actool.CoreTool {
	return t.readExpTool("list_results", "分页查看本任务的通用成果和独立验证记录。关联任务知识仍可通过 list_facts/node_detail 只读查看。成果与验证 ID 均为探索节点 ID。",
		obj(map[string]any{"before": intp("上一页 next_before，缺省最新"), "limit": intp("默认 30，最多 100")}),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var input struct {
				Before int64
				Limit  int
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return actool.Errorf("无效 JSON"), nil
			}
			nodes, more, err := t.ts.WorkRecords(input.Before, input.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := map[string]any{"records": nodes, "has_more": more}
			if more && len(nodes) > 0 {
				out["next_before"] = nodes[len(nodes)-1].ID
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) recordResult() actool.CoreTool {
	return t.writeExpTool("record_result", "登记通用成果：分析、实现、实验或报告。files 会保存本任务文件的独立内容快照和 SHA256；后续修改原文件不会改变该版本。登记不等于验证通过。",
		obj(map[string]any{"summary": str("成果摘要"), "result_type": stringEnum("成果种类", "analysis", "implementation", "experiment", "report", "blocker"),
			"detail": str("结论、适用版本、位置、条件和未知项"), "evidence": str("实际命令/输出或可核对的依据"),
			"confidence": stringEnum("观察或推断", "observed", "inferred"),
			"files":      map[string]any{"type": "array", "items": str("相对当前工作目录或本任务内的绝对路径"), "maxItems": 8}}, "summary", "result_type", "evidence", "confidence"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var input struct {
				Summary, Detail, Evidence, Confidence string
				Type                                  string `json:"result_type"`
				Files                                 []string
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return actool.Errorf("无效 JSON"), nil
			}
			if strings.TrimSpace(input.Summary) == "" || strings.TrimSpace(input.Evidence) == "" || len(input.Files) > 8 || len(input.Summary) > 4000 || len(input.Detail)+len(input.Evidence) > 64000 {
				return actool.Errorf("摘要、证据必填；说明不超过 64000 字节，文件最多 8 个"), nil
			}
			if input.Confidence != "observed" && input.Confidence != "inferred" {
				return actool.Errorf("confidence 必须为 observed/inferred"), nil
			}
			switch input.Type {
			case "analysis", "implementation", "experiment", "report", "blocker":
			default:
				return actool.Errorf("result_type 无效"), nil
			}
			if t.workspaceRoot == "" || t.runIntentID <= 0 {
				return actool.Errorf("仅通用任务的执行工作可登记成果"), nil
			}
			files := []workproduct.Artifact{}
			for _, path := range input.Files {
				file, err := workproduct.Capture(ctx, t.workspaceRoot, t.workspaceDir, path)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				files = append(files, file)
			}
			payload := map[string]any{"record_type": "result", "result_type": input.Type, "summary": input.Summary,
				"detail": input.Detail, "evidence": input.Evidence, "confidence": input.Confidence, "files": files, "producer_intent_id": t.runIntentID}
			id, err := t.ts.RecordWorkFact(payload, t.runIntentID, t.worker)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.workFactNotice(id, input.Summary)
			return jsonResult(map[string]any{"result_id": id, "digest": payload["digest"], "files": files, "task_dir": t.workspaceRoot})
		})
}

func (t *ToolSet) recordValidation() actool.CoreTool {
	return t.writeExpTool("record_validation", "记录已实际执行的独立验证；本工具不会运行 evidence 里的命令。须由 verifier/integrator 验证他人的本任务成果，并提供准确 digest；缺少可运行环境时记录 inconclusive。文件快照会重新校验。",
		obj(map[string]any{"result_id": idp("本任务成果 ID"), "digest": str("成果返回的精确版本摘要"),
			"verdict": stringEnum("本次结论", "passed", "failed", "inconclusive"), "summary": str("验证结论与覆盖边界"), "evidence": str("实际检查命令、环境、输出、对照或反例")}, "result_id", "digest", "verdict", "summary", "evidence"),
		func(_ context.Context, raw json.RawMessage) (actool.Result, error) {
			var input struct {
				ResultID                           int64 `json:"result_id"`
				Digest, Verdict, Summary, Evidence string
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return actool.Errorf("无效 JSON"), nil
			}
			if t.workspaceRoot == "" || t.runIntentID <= 0 || (t.workRole != "verifier" && t.workRole != "integrator") {
				return actool.Errorf("需要独立 verifier/integrator 工作"), nil
			}
			if strings.TrimSpace(input.Summary) == "" || strings.TrimSpace(input.Evidence) == "" || len(input.Summary) > 4000 || len(input.Evidence) > 64000 {
				return actool.Errorf("摘要、真实检查证据必填且长度须受限"), nil
			}
			if input.Verdict != "passed" && input.Verdict != "failed" && input.Verdict != "inconclusive" {
				return actool.Errorf("verdict 无效"), nil
			}
			node, err := t.ts.GetNode(input.ResultID)
			if err != nil || node == nil || node.Kind != db.KindFact {
				return actool.Errorf("result_id 必须是本任务成果"), nil
			}
			var result struct {
				RecordType string `json:"record_type"`
				Digest     string
				Producer   int64 `json:"producer_intent_id"`
				Files      []workproduct.Artifact
			}
			if err := json.Unmarshal(node.Payload, &result); err != nil {
				return actool.Errorf("成果记录无效"), nil
			}
			if result.RecordType != "result" || input.Digest == "" || result.Digest != input.Digest || result.Producer <= 0 || result.Producer == t.runIntentID {
				return actool.Errorf("成果版本不匹配或不能自我验收"), nil
			}
			for _, artifact := range result.Files {
				f, err := workproduct.OpenVerified(t.workspaceRoot, artifact)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				f.Close()
			}
			payload := map[string]any{"record_type": "validation", "result_id": input.ResultID, "result_digest": input.Digest,
				"verdict": input.Verdict, "summary": input.Summary, "evidence": input.Evidence, "confidence": "observed", "validator_intent_id": t.runIntentID}
			id, err := t.ts.RecordWorkFact(payload, t.runIntentID, t.worker)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			t.workFactNotice(id, input.Summary)
			return jsonResult(map[string]any{"validation_id": id, "verdict": input.Verdict})
		})
}

func (t *ToolSet) workFactNotice(id int64, summary string) {
	t.writes.Facts++
	if t.notifyFact != nil {
		t.notifyFact(t.runIntentID, fmt.Sprintf("#%d %s", id, summary))
	}
}

func (t *ToolSet) prepareWorktree() actool.CoreTool {
	return t.writeExpTool("prepare_worktree", "为本工作从本地 Git 仓库 HEAD 创建独立 worktree/分支，返回精确基线。不会清理或重置原仓库；已存在时拒绝覆盖。后续所有修改/检查在返回路径执行。",
		obj(map[string]any{"repo": str("已存在的本地仓库绝对路径")}, "repo"),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var input struct{ Repo string }
			if err := json.Unmarshal(raw, &input); err != nil {
				return actool.Errorf("无效 JSON"), nil
			}
			if t.workspaceRoot == "" || t.runIntentID <= 0 {
				return actool.Errorf("仅通用任务的执行工作可创建工作区"), nil
			}
			result, err := workproduct.PrepareWorktree(ctx, input.Repo, t.workspaceRoot, t.runIntentID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(result)
		})
}
