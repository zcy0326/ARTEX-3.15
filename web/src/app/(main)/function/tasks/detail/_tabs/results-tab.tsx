"use client";

import * as React from "react";
import { DownloadIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { WorkRecord } from "@/lib/types";

const types: Record<string, string> = { analysis: "分析", implementation: "实现", experiment: "实验", report: "报告" };
const verdicts: Record<string, string> = { passed: "通过", failed: "未通过", inconclusive: "无法确认" };

export function ResultsTab({ taskId }: { taskId: string }) {
  const [records, setRecords] = React.useState<WorkRecord[]>([]);
  const [next, setNext] = React.useState<number>();
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const request = React.useRef(0);

  const load = React.useCallback(
    async (before?: number) => {
      const sequence = ++request.current;
      setLoading(true);
      try {
        const page = await api.workResults(taskId, before);
        if (sequence !== request.current) return;
        setRecords((previous) => (before ? [...previous, ...page.records] : page.records));
        setNext(page.has_more ? page.next_before : undefined);
        setError("");
      } catch (e) {
        if (sequence === request.current) setError((e as Error).message);
      } finally {
        if (sequence === request.current) setLoading(false);
      }
    },
    [taskId],
  );

  React.useEffect(() => {
    void load();
    return () => {
      request.current++;
    };
  }, [load]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="font-semibold">成果与验证</h2>
          <p className="text-muted-foreground mt-1 text-sm">
            分析、实现、实验和报告均可交付。文件保留独立快照；验证针对精确版本，成果登记与验证通过分别展示。
          </p>
        </div>
        <Button variant="outline" size="sm" disabled={loading} onClick={() => void load()}>
          <RefreshCwIcon />
          刷新
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {!loading && !error && records.length === 0 && (
        <Card>
          <CardContent className="text-muted-foreground p-6 text-sm">
            尚无成果。执行者完成分析或实现后会在这里保存结论、文件版本与证据。
          </CardContent>
        </Card>
      )}
      {records.map((record) => {
        const p = record.payload;
        const validation = p.record_type === "validation";
        return (
          <Card key={record.id}>
            <CardContent className="space-y-3 p-4">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline">
                  #{record.id} · {validation ? "独立验证" : (types[p.result_type ?? ""] ?? "成果")}
                </Badge>
                {validation && (
                  <Badge variant={p.verdict === "failed" ? "destructive" : "secondary"}>
                    {verdicts[p.verdict ?? ""] ?? p.verdict}
                  </Badge>
                )}
                {!validation && <Badge variant="secondary">{p.confidence === "inferred" ? "推断" : "已观察"}</Badge>}
                <span className="text-muted-foreground text-xs">
                  工作 #{p.producer_intent_id ?? p.validator_intent_id} ·{" "}
                  {new Date(record.created_at).toLocaleString("zh-CN")}
                </span>
              </div>
              <p className="text-sm font-medium">{p.summary}</p>
              {validation && (
                <p className="text-muted-foreground text-xs">
                  对应成果 #{p.result_id} · <code className="break-all">{p.result_digest}</code>
                </p>
              )}
              {p.digest && (
                <p className="text-muted-foreground text-xs">
                  版本 <code className="break-all">{p.digest}</code>
                </p>
              )}
              {p.detail && (
                <details>
                  <summary className="cursor-pointer text-sm">分析与适用条件</summary>
                  <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap rounded border p-3 text-xs">
                    {p.detail}
                  </pre>
                </details>
              )}
              {p.evidence && (
                <details>
                  <summary className="cursor-pointer text-sm">证据与检查记录</summary>
                  <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap rounded border p-3 text-xs">
                    {p.evidence}
                  </pre>
                </details>
              )}
              {p.files?.map((file, index) => (
                <div
                  key={`${file.sha256}-${index}`}
                  className="flex flex-wrap items-center justify-between gap-2 rounded border p-3"
                >
                  <div className="min-w-0">
                    <p className="break-all text-sm">
                      {file.name} · {file.size.toLocaleString()} 字节
                    </p>
                    <code className="text-muted-foreground break-all text-xs">SHA256 {file.sha256}</code>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={async () => {
                      try {
                        await api.downloadWorkArtifact(taskId, record.id, index, file.name);
                      } catch (e) {
                        toast.error((e as Error).message);
                      }
                    }}
                  >
                    <DownloadIcon />
                    下载快照
                  </Button>
                </div>
              ))}
            </CardContent>
          </Card>
        );
      })}
      {next && (
        <Button variant="outline" disabled={loading} onClick={() => void load(next)}>
          加载更早记录
        </Button>
      )}
      {loading && (
        <p className="text-muted-foreground text-sm" role="status">
          加载中…
        </p>
      )}
    </div>
  );
}
