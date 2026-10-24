// 共通の fixture（platform.md §10.2）。テストは `import { test, expect } from "../../fixtures/index.ts"` で使う。
import { test as base, expect } from "@playwright/test";
import { createApi, type Api } from "./api.ts";
import { createClock, type Clock } from "./clock.ts";
import { createDb, type Db } from "./db.ts";
import { attachOnFailure, createLogs, newCorrelationId, type Logs } from "./logs.ts";
import { createMail, type Mail } from "./mail.ts";
import { createSink, type Sink } from "./sink.ts";
import { createTokens, type Tokens } from "./tokens.ts";
import { createWait, type Wait } from "./wait.ts";
import { createWorld, type World } from "./world.ts";

interface Fixtures {
  logs: Logs;
  api: Api;
  tokens: Tokens;
  db: Db;
  wait: Wait;
  clock: Clock;
  sink: Sink;
  mail: Mail;
  world: World;
}

export const test = base.extend<Fixtures>({
  logs: [
    async ({}, use, testInfo) => {
      const corr = newCorrelationId(testInfo);
      const started = new Date();
      await use(createLogs(corr));
      await attachOnFailure(testInfo, corr, started);
    },
    { auto: true },
  ],
  api: async ({ request, logs }, use) => {
    await use(createApi(request, logs.correlationId));
  },
  // ブラウザのテストでも、API への要求に同じ相関 ID を付ける（page を使うテストだけで動く）
  page: async ({ page, logs }, use) => {
    const apiBase = process.env.API_BASE_URL ?? "http://localhost:8080";
    await page.route(`${apiBase}/**`, (route) => route.continue({ headers: { ...route.request().headers(), "x-correlation-id": logs.correlationId } }));
    await use(page);
  },
  tokens: async ({ api }, use, testInfo) => {
    await use(createTokens(api, testInfo.testId.slice(0, 10)));
  },
  db: async ({}, use) => {
    await use(createDb());
  },
  wait: async ({ db }, use, testInfo) => {
    await use(createWait(db, testInfo.project.name === "clock" ? 60_000 : 15_000));
  },
  clock: async ({ api }, use, testInfo) => {
    await use(createClock(api, testInfo));
  },
  sink: async ({ request }, use, testInfo) => {
    const s = createSink(request, testInfo.project.name === "clock" ? 60_000 : 15_000);
    await use(s);
    await s.cleanup();
  },
  mail: async ({ db, wait, request }, use) => {
    await use(createMail(db, wait, request));
  },
  world: async ({ api, tokens, wait }, use, testInfo) => {
    await use(createWorld(api, tokens, wait, testInfo.testId.slice(0, 10)));
  },
});

export { expect };
