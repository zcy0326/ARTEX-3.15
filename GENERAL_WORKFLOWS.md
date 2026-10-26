# 通用研究与开发任务

新建任务时选择“通用研究与开发”，填写材料、目标和验收条件。已有任务和未传 `workflow` 的 API 调用保持安全测试模式。

## 能力

- 通用目标拆解与主控/规划/执行提示词，支持源码研究、逆向分析、实现、实验和报告。
- 每项工作指定 `analyst`、`implementer`、`verifier` 或 `integrator`。沿用现有 worker 池、任务 LLM 配置链和 worker 的 MCP/Skill 绑定，按工作切换职责。
- `depends_on` 等待前置工作正常结束；`verified_result_ids` 等待指定成果版本的最新独立验证通过。前置失败、产生 blocker 或被删除时不自动放行。
- `record_result` 保存结论、证据、置信状态和文件版本。文件复制到任务内的内容寻址快照，原文件后续修改不改变旧版本。
- `record_validation` 要求不同工作的 verifier/integrator 提交准确成果 ID、版本摘要和实际检查证据。通过、失败与无法确认分别记录。
- 实现成果的目标验收要求对应版本最新独立验证通过；推断、阻塞和失败验证不能直接完成目标。
- `prepare_worktree` 为本地仓库创建当前工作的独立分支与工作副本，返回精确基线；不清理原仓库或覆盖已有工作区。
- 通用任务显示目标与材料、工作与分工、成果与验证。安全任务保留原有资产、漏洞、流量、复测、覆盖度和报告页面。

## 示例

任务：分析给定程序的文件格式，开发解析器，并通过已有样本和边界用例验证。

1. 规划者创建分析工作，明确输入、输出和验收。
2. 分析者通过实际工具形成结论，保留位置、样本版本、依据和未知项。
3. 实现者等待所需分析完成，在独立 worktree 开发，将关键代码、diff 或报告登记为成果快照。
4. 规划者在成果真实产生后创建验证工作。验证者读取准确版本，使用 Bash 或已配置工具执行检查，再登记结论。
5. 需要已验证输入的后续工作使用 `verified_result_ids`。集成产生新的成果版本，重新验证该版本。

工具目录仍尊重禁用和 agent 绑定。通用工具在最终装配阶段按 workflow 选择，避免修改安全任务的默认工具集。缺少工具或环境时记录 `result_type=blocker`，说明缺口。

## API

`POST /api/tasks` 在原字段之外接受：

```json
{
  "workflow": "general",
  "description": "材料位于本地仓库 /workspace/example，输入样本已上传",
  "goal": "实现可运行的解析器并交付分析说明",
  "acceptance": "已有样本全部解析成功；边界用例有检查记录；交付源码与说明"
}
```

任务模板保存 workflow 和验收条件。`spawn_task` 可指定这些字段；省略 workflow 时继承显式父任务，否则使用 security。

- `GET /api/tasks/{id}/work`：最近 200 项工作。
- `GET /api/tasks/{id}/results?before=...`：成果与验证记录，每页 30 项。
- `GET /api/tasks/{id}/results/{result}/files/{index}`：经鉴权和 SHA256 校验的快照下载；无法跨任务下载。

这些成果沿用事实节点与原血缘存储。已有 `list_facts`、`node_detail`、团队交接和归档能保留记录；工作依赖是独立的 `requires` / `requires_validation` 边。

## 验证与运行边界

验证记录不是命令执行器：验证者须先实际运行工具，再提交观察。平台检查角色、作者身份、版本、文件完整性和最新结论，不保证证据文本本身一定真实。独立工作可以使用相同模型，不等于模型供应商层面的独立。

每个成果最多附 8 个文件，每个文件最多 64 MiB。文件须位于当前任务目录内；外部输入可由已有 Read/Bash/MCP 分析，交付物复制或生成到本任务目录再登记。快照缺失或被改动时拒绝下载和沿用验证。

工作区工具针对已存在的本地 Git 仓库。创建后的修改、提交、集成和检查由 agent 使用已有工具完成；本 PR 不自动合并分支，也不清理用户仓库的工作树或分支。工作重试时复用此前返回的路径，重复创建会明确失败。

本 PR 未内置反编译器、远端设备 runner、独立项目对象库或跨进程资源租约。当前可通过已有 MCP/Skill 接入专业工具；后续架构路线见 [GENERAL_PLATFORM_DESIGN.md](GENERAL_PLATFORM_DESIGN.md)。

## 检查

```sh
go test ./... -run TestNonexistent -count=1
go vet ./...
go test -race -p 1 ./workproduct ./db ./agent ./server -run 'TestGeneral|TestTeam|TestFactNotice' -count=1 -v
```

数据库测试需要 `ARTEX_PG_DSN`。GitHub Actions 的 General workflows 使用 PostgreSQL 16，并运行前端类型检查与生产构建。模型驱动的真实任务表现仍需在相同模型、输入和预算下评估；单元/集成检查不代表通用逆向成功率。
