// clock fixture：dev clock を進める・ジョブを手動で実行する（platform.md §10.2、P-TST-9）。
// dev clock はシステム全体で 1 つなので、*.clock.spec.ts（clock project。1 件ずつ直列）以外から呼ぶと例外にする。
import type { TestInfo } from "@playwright/test";
import type { Api } from "./api.ts";

export interface JobRun {
  run_id: string;
  job: string;
  status: "queued" | "running" | "succeeded" | "failed";
  as_of: string;
  attempts: number;
  stats: Record<string, number> | null;
  error: string | null;
  deduplicated: boolean;
}

export interface Clock {
  advance(duration: string): Promise<{ now: string; today: string }>;
  setTo(time: string | Date): Promise<{ now: string; today: string }>;
  runJob(name: string, opts?: { asOf?: string; params?: object; force?: boolean }): Promise<JobRun>;
  now(): Promise<{ now: string; today: string }>;
}

export class ClockMisuseError extends Error {}

function guard(testInfo: TestInfo, op: string) {
  if (!testInfo.file.endsWith(".clock.spec.ts")) {
    throw new ClockMisuseError(`clock.${op} は *.clock.spec.ts でだけ使えます（P-TST-9）: ${testInfo.file}`);
  }
}

async function waitUntil(iso: string | undefined) {
  if (!iso) return;
  const ms = new Date(iso).getTime() - Date.now();
  if (ms > 0) await new Promise((r) => setTimeout(r, ms + 50)); // effective_after を過ぎてから次の操作をする（E14）
}

export function createClock(api: Api, testInfo: TestInfo): Clock {
  const set = async (body: { advance?: string; to?: string }) => {
    const res = await api.dev.setClock(body);
    if (res.status !== 200) throw new Error(`dev clock: ${res.status} ${JSON.stringify(res.body)}`);
    const st = res.body as { now: string; today: string; effective_after?: string };
    await waitUntil(st.effective_after);
    return st;
  };
  return {
    advance: async (duration) => {
      guard(testInfo, "advance");
      return set({ advance: duration });
    },
    setTo: async (time) => {
      guard(testInfo, "setTo");
      return set({ to: typeof time === "string" ? time : time.toISOString() });
    },
    runJob: async (name, opts = {}) => {
      guard(testInfo, "runJob");
      const res = await api.dev.runJob(name, { as_of: opts.asOf, params: opts.params, force: opts.force, wait_seconds: 120 });
      if (res.status !== 200) throw new Error(`runJob ${name}: ${res.status} ${JSON.stringify(res.body)}`);
      const run = res.body as JobRun;
      if (run.status !== "succeeded") throw new Error(`runJob ${name}: ${run.status} ${run.error ?? ""}`);
      return run;
    },
    now: async () => (await api.dev.getClock()).body as { now: string; today: string },
  };
}
