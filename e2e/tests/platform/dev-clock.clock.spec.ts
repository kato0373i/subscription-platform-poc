// AC-PLT-039・040・041・082：dev clock とジョブの手動実行
import { test, expect } from "../../fixtures/index.ts";

function addDays(date: string, n: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}

test("72 時間進めると、その後に実行したジョブの as_of が 3 日進む", async ({ clock, db }) => {
  const before = await clock.now();
  const started = Date.now();
  await clock.advance("72h");
  // POST /v1/dev/clock は書き込んだらすぐ返り、effective_after（応答 + 1 秒）を待つのは fixture
  expect(Date.now() - started).toBeLessThan(5_000);
  const run = await clock.runJob("platform-noop", { force: true });
  expect(run.as_of).toBe(addDays(before.today, 3));
  const rows = await db.query<{ status: string }>("SELECT status FROM platform.job_runs WHERE id = $1", [run.run_id]);
  expect(rows[0]?.status).toBe("succeeded");
});

test("現在より前には戻せない", async ({ api }) => {
  const before = (await api.dev.getClock()).body as { now: string; version: number };
  const res = await api.dev.setClock({ to: new Date(new Date(before.now).getTime() - 3600_000).toISOString() });
  expect(res.status).toBe(422);
  expect(res.problem?.code).toBe("clock_backward_not_allowed");
  const after = (await api.dev.getClock()).body as { version: number };
  expect(after.version).toBe(before.version);
});

test("登録済みのジョブは実行され、未登録のジョブは 404", async ({ clock, api }) => {
  const run = await clock.runJob("platform-noop", { force: true });
  expect(run.status).toBe("succeeded");
  const res = await api.dev.runJob("platform-no-such-job");
  expect(res.status).toBe(404);
  expect(res.problem?.code).toBe("not_found");
});

test("手動実行したジョブのログとイベントは、呼び出し側の相関 ID を持つ", async ({ clock, logs, db, wait }) => {
  const run = await clock.runJob("platform-noop", { force: true });
  const lines = await wait.until(async () => {
    const l = await logs.query({ msg: "job." });
    return l.some((x) => x.msg === "job.completed") ? l : undefined;
  });
  expect(lines.map((l) => l.msg)).toEqual(expect.arrayContaining(["job.started", "job.completed"]));
  for (const l of lines) expect(l.job_run_id).toBe(run.run_id);
  const ev = await db.query<{ correlation_id: string; causation_id: string }>(
    "SELECT correlation_id, causation_id FROM platform.outbox_events WHERE event_type = 'platform.job_noop_ran' AND aggregate_id = $1",
    [run.run_id],
  );
  expect(ev).toHaveLength(1);
  expect(ev[0].correlation_id).toBe(logs.correlationId);
  expect(ev[0].causation_id).toBe(run.run_id);
});
