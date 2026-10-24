// logs fixture（自動で使われる。platform.md §10.2・§8.6）。
// テストごとに相関 ID を作って api に渡し、テストが失敗したら、その相関 ID で絞ったログをレポートに添付する。
import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import { promisify } from "node:util";
import { randomBytes } from "node:crypto";
import type { TestInfo } from "@playwright/test";

const run = promisify(execFile);

export interface LogFilter {
  corr?: string;
  msg?: string;
  service?: string;
  level?: string;
  since?: string;
  errors?: boolean;
}

export interface LogLine {
  ts: string;
  level: string;
  msg: string;
  service: string;
  correlation_id?: string;
  [key: string]: unknown;
}

export interface Logs {
  /** このテストの相関 ID（e2e-<testId>-<乱数 6 文字>）。 */
  correlationId: string;
  /** 自分のログを devlogs で読む（JSON の行）。既定の相関 ID はこのテストのもの。 */
  query(filter?: LogFilter): Promise<LogLine[]>;
}

function devlogsCommand(): { cmd: string; args: string[] } {
  const bin = process.env.DEVLOGS_BIN ?? "../.local/bin/devlogs";
  if (existsSync(bin)) return { cmd: bin, args: [] };
  return { cmd: "go", args: ["run", "../cmd/devlogs"] };
}

export async function devlogs(args: string[]): Promise<string> {
  const { cmd, args: pre } = devlogsCommand();
  const dir = process.env.LOG_DIR ?? "../.local/logs";
  // サブコマンド（cwl）は先頭に置く。ファイルを読むときだけ --dir を付ける
  const full = args[0] === "cwl" ? [...pre, ...args] : [...pre, "--dir", dir, ...args];
  const { stdout } = await run(cmd, full, { maxBuffer: 64 << 20 });
  return stdout;
}

function filterArgs(f: LogFilter): string[] {
  const a: string[] = [];
  if (f.corr) a.push("--corr", f.corr);
  if (f.msg) a.push("--msg", f.msg);
  if (f.service) a.push("--service", f.service);
  if (f.level) a.push("--level", f.level);
  if (f.since) a.push("--since", f.since);
  if (f.errors) a.push("--errors");
  return a;
}

export function newCorrelationId(testInfo: TestInfo): string {
  const id = testInfo.testId.replace(/[^A-Za-z0-9._:-]/g, "").slice(0, 80);
  return `e2e-${id}-${randomBytes(3).toString("hex")}`;
}

export function createLogs(correlationId: string): Logs {
  return {
    correlationId,
    async query(filter: LogFilter = {}) {
      const out = await devlogs([...filterArgs({ corr: correlationId, ...filter }), "--format=json"]);
      return out
        .split("\n")
        .filter((l) => l.trim() !== "")
        .map((l) => JSON.parse(l) as LogLine);
    },
  };
}

/** テストが失敗したら logs.jsonl・timeline.txt・errors.txt を添付する。 */
export async function attachOnFailure(testInfo: TestInfo, correlationId: string, startedAt: Date): Promise<void> {
  if (testInfo.status === testInfo.expectedStatus) return;
  await new Promise((r) => setTimeout(r, 500)); // プロセスの書き込みを取りこぼさない
  try {
    const json = await devlogs(["--corr", correlationId, "--format=json"]);
    const timeline = await devlogs(["--corr", correlationId, "--format=timeline"]);
    const errors = await devlogs(["--errors", "--since", startedAt.toISOString(), "--format=timeline"]);
    await testInfo.attach("logs.jsonl", { body: json, contentType: "application/x-ndjson" });
    await testInfo.attach("timeline.txt", { body: timeline, contentType: "text/plain" });
    await testInfo.attach("errors.txt", { body: errors, contentType: "text/plain" });
  } catch (e) {
    await testInfo.attach("logs-error.txt", { body: String(e), contentType: "text/plain" });
  }
}
