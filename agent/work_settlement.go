package agent

import (
	"context"
	"github.com/Autumn-27/norma/harness"
)

func workflowSettlement(ctx context.Context, key string, settlement *harness.Settlement) *harness.Settlement {
	if !isGeneral(ctx) || settlement == nil {
		return settlement
	}
	copy := *settlement
	switch key {
	case "worker":
		copy.Prompt = "本次工作预算已耗尽。停止新命令与实验，把已有成果用 record_result 保存，缺失材料或环境用 blocker 说明。只记录真实证据，未完成和未验证部分明确交接，最后用一句话总结。"
		copy.DisabledTools = append(append([]string{}, copy.DisabledTools...), "prepare_worktree", "shell_open", "shell_send")
	case "planner":
		copy.Prompt = "本轮规划预算已耗尽。只保存已有计划：有必要且前置明确的工作用 add_work，依据实际成果与独立验证用 prove_goal。缺少输入时明确等待，不为填满队列而生成工作。然后结束本轮。"
	}
	if len(settlement.PromptByReason) > 0 {
		copy.PromptByReason = make(map[harness.TerminalReason]string, len(settlement.PromptByReason))
		for reason := range settlement.PromptByReason {
			copy.PromptByReason[reason] = "任务预算已到期，停止新增工作和命令。保存已经取得的通用成果与真实证据，对照验收条件如实记录完成情况；不把未验证内容当成功。"
		}
	}
	return &copy
}
