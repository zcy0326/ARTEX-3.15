# 团队资料交接与增量共享

worker 启动时，系统会读取其 `derived_from` 前置节点、同一目标资产上的队友事实与漏洞，以及无资产锚点的任务级观察，连同来源意图、证据、置信度一起交接。前置节点优先。资料可来自本任务及显式关联的直接来源任务；不递归读取来源任务的来源，也不读取无关任务。

执行过程中，工具正常返回后会检查相关队友新资料，并通过独立会话消息送达。每个执行实例最多每 10 秒查询一次，每次读取最多 12 个节点、总正文预算 10,000 字符；没有工具返回时不会后台打断模型。工具本身的返回、错误标记、已有额外消息和权限检查保持原样。读取失败或取消不会改变原工具结果。

单字段长内容会截断并提供 `node_detail(id)` 恢复入口。未展示的节点不计为已读，后续仍可交接。去重按实际已交付节点 ID 进行，不使用“最大 ID”水位，避免数据库事务乱序提交导致较小 ID 的资料被永久漏掉。去重状态属于本次执行，续跑时会重新从数据库构建启动资料。

普通 `record_fact` 成功后也会唤醒 planner，不必等待整个 worker 结束；批量写入只通知成功保存的条目，同一来源意图在 debounce 期间合并通知。`report_finding` 继续沿用原有即时通知。worker 漏填 `intent_id` 时，事实与漏洞默认使用程序保存的当前执行意图；显式填写其他 worker 的意图 ID 会被拒绝。

worker 默认新增 `list_facts`、`list_worker_traces`，可主动取回队友过程，仍不拥有 `add_intent`、`set_goals`、`add_task_scope` 或企业范围管理工具。旧数据库仅升级历史默认绑定与原始工具说明；用户的禁用开关、自定义绑定、提示词和后续解绑不被重置。

共享内容是数据，不能覆盖任务范围、操作约束或工具权限。`inferred` 不升级为 `observed`，缺少置信度标为 `unspecified`，血缘上的前置来源也不等于已经验证的执行条件。这次改动没有新增自动利用策略、独立复测必经步骤或形式化权限状态机。

验证：

```bash
go test -race ./agent ./db ./server -run 'TestTeam|TestFactNotice' -count=1
ARTEX_PG_DSN='postgres://artex:artex_test@localhost:5432/artex_test?sslmode=disable' \
  go test -race -p 1 ./agent ./db ./server -count=1
```

对应 GitHub Actions 工作流提供 PostgreSQL 16，运行上述三个包的完整竞态与集成测试。纯资料渲染、限流、去重、并发交付和通知合并测试不需要数据库。
