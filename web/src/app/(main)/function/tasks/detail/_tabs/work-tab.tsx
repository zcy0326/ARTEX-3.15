"use client";

import * as React from "react";
import { RefreshCwIcon } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Task, WorkRecord } from "@/lib/types";
import { WorkInputs } from "./work-inputs";

const roles: Record<string, string> = { analyst: "分析", implementer: "实现", verifier: "验证", integrator: "集成" };
const states: Record<string, string> = {
  open: "等待执行或前置",
  running: "执行中",
  done: "执行结束",
  blocked: "执行出错",
  exhausted: "预算耗尽",
  stopped: "已停止",
  paused: "已暂停",
  deleted: "已移除",
};

export function GeneralOverview({ task }: { task: Task }) {
  return (
    <div className="grid gap-4">
      {[
        { title: "任务目标", content: task.goal },
        { title: "材料与背景", content: task.description },
        { title: "验收条件", content: task.acceptance || "规划者将根据目标建立各项工作的验收条件。" },
      ].map((item) => (
        <Card key={item.title}>
          <CardContent className="space-y-2 p-5">
            <h2 className="font-semibold">{item.title}</h2>
            <p className="whitespace-pre-wrap text-sm">{item.content}</p>
          </CardContent>
        </Card>
      ))}
      <p className="text-muted-foreground text-sm">
        在“工作与分工”查看职责和前置，在“成果与验证”查看交付物、版本与独立检查。可通过主控会话补充目标和约束。
        可从已有项目、PoC或逆向成果开始，按需求复现、适配、扩展和集成。
      </p>
    </div>
  );
}

export function WorkTab({ taskId }: { taskId: string }) {
  const [work, setWork] = React.useState<WorkRecord[]>([]);
  const [error, setError] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const sequence = React.useRef(0);
  const load = React.useCallback(async () => {
    const current = ++sequence.current;
    setLoading(true);
    try {
      const rows = await api.taskWork(taskId);
      if (current === sequence.current) {
        setWork(rows);
        setError("");
      }
    } catch (e) {
      if (current === sequence.current) setError((e as Error).message);
    } finally {
      if (current === sequence.current) setLoading(false);
    }
  }, [taskId]);
  React.useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 10000);
    return () => {
      clearInterval(timer);
      sequence.current++;
    };
  }, [load]);
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="font-semibold">工作与分工</h2>
        <Button variant="outline" size="sm" disabled={loading} onClick={() => void load()}>
          <RefreshCwIcon />
          刷新
        </Button>
      </div>
      <p className="text-muted-foreground text-sm">
        按需要分配分析、实现、验证或集成。工作结束不等于验收通过；验证前置只接受对应成果版本的最新通过结论。
      </p>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {!loading && !error && work.length === 0 && (
        <p className="text-muted-foreground text-sm">尚无工作，等待目标拆解与首轮规划。</p>
      )}
      {work.map((item) => (
        <Card key={item.id}>
          <CardContent className="space-y-3 p-4">
            <div className="flex flex-wrap gap-2">
              <Badge variant="outline">工作 #{item.id}</Badge>
              <Badge variant="secondary">{roles[item.payload.work_role ?? ""] ?? "执行"}</Badge>
              <span className="text-muted-foreground text-sm">{states[item.state] ?? item.state}</span>
            </div>
            <p className="whitespace-pre-wrap text-sm font-medium">{item.payload.summary}</p>
            {item.payload.acceptance && <p className="whitespace-pre-wrap text-sm">验收：{item.payload.acceptance}</p>}
            <WorkInputs inputs={item.payload.inputs} />
            {!!item.payload.depends_on?.length && (
              <p className="text-muted-foreground text-xs">
                等待工作结束：{item.payload.depends_on.map((id) => `#${id}`).join("、")}
              </p>
            )}
            {!!item.payload.verified_result_ids?.length && (
              <p className="text-muted-foreground text-xs">
                等待成果独立验证通过：{item.payload.verified_result_ids.map((id) => `#${id}`).join("、")}
              </p>
            )}
          </CardContent>
        </Card>
      ))}
      {work.length >= 200 && (
        <p className="text-muted-foreground text-sm">仅显示最近 200 项工作；完整过程可在会话和链路中回看。</p>
      )}
    </div>
  );
}
