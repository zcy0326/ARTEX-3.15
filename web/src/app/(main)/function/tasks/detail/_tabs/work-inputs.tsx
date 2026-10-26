import { Badge } from "@/components/ui/badge";
import type { WorkInput } from "@/lib/types";

const kinds: Record<WorkInput["kind"], string> = {
  repository: "已有项目",
  poc: "PoC",
  reverse_analysis: "逆向结论",
  result: "已有成果",
  finding: "漏洞依据",
  reference: "参考材料",
};

export function WorkInputs({ inputs }: { inputs?: WorkInput[] }) {
  if (!inputs?.length) return null;
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium">复用材料与用途</p>
      {inputs.map((input, index) => (
        <div
          key={`${input.node_id ?? input.locator}-${index}`}
          className="space-y-1 rounded border p-3 text-xs"
        >
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{kinds[input.kind] ?? input.kind}</Badge>
            {!!input.node_id && <span>来源节点 #{input.node_id}</span>}
            {!!input.source_task_id && (
              <span>关联任务 #{input.source_task_id} · 只读引用</span>
            )}
          </div>
          <p className="whitespace-pre-wrap text-sm">{input.purpose}</p>
          {input.locator && (
            <p className="text-muted-foreground break-all">
              位置：{input.locator}
            </p>
          )}
          {input.revision && (
            <p className="text-muted-foreground break-all">
              声明版本（需实测核对）：{input.revision}
            </p>
          )}
          {input.node_digest && (
            <p className="text-muted-foreground break-all">
              规划时节点版本：<code>{input.node_digest}</code>
            </p>
          )}
        </div>
      ))}
      <p className="text-muted-foreground text-xs">
        来源引用用于追溯，材料的适用性与成品行为须分别验证。
      </p>
    </div>
  );
}
