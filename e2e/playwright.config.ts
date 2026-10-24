// Playwright の設定（platform.md §10.2）。
//   parallel: 時刻を進めないテスト（並列）
//   clock:    *.clock.spec.ts（dev clock はシステム全体で 1 つなので、parallel の後に 1 件ずつ直列）
//   perf:     *.perf.ts（既定の実行には含めない。pnpm e2e:perf）
//   selftest: わざと失敗するテスト（task e2e:selftest だけが流す）
import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";

if (existsSync(new URL("./.env.e2e", import.meta.url))) {
  process.loadEnvFile(new URL("./.env.e2e", import.meta.url));
}

const CI = !!process.env.CI;

export default defineConfig({
  timeout: 60_000,
  expect: { timeout: 10_000 },
  retries: 0, // 不安定なテストを隠さない
  forbidOnly: CI,
  reporter: [
    ["html", { open: "never", outputFolder: "playwright-report" }],
    ["junit", { outputFile: "test-results/junit.xml" }],
    ["list"],
  ],
  use: {
    baseURL: process.env.API_BASE_URL ?? "http://localhost:8080",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "parallel",
      testDir: "tests",
      testMatch: /\.spec\.ts$/,
      testIgnore: /\.clock\.spec\.ts$/,
      fullyParallel: true,
      workers: CI ? 4 : "50%",
    },
    {
      name: "clock",
      testDir: "tests",
      testMatch: /\.clock\.spec\.ts$/,
      fullyParallel: false,
      workers: 1,
      timeout: 180_000,
      dependencies: ["parallel"],
    },
    {
      name: "perf",
      testDir: "tests",
      testMatch: "**/*.perf.ts",
      workers: 1,
      timeout: 60 * 60_000,
    },
    {
      name: "selftest",
      testDir: "selftest",
      testMatch: /\.spec\.ts$/,
      workers: 1,
    },
  ],
});
