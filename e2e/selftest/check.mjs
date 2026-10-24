// task e2e:selftest：わざと失敗するテストを流し、JSON のレポートで添付（logs.jsonl・timeline.txt・errors.txt）を検査する（AC-PLT-074）。
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";

let report;
try {
  execFileSync("npx", ["-y", "pnpm@9.15.9", "exec", "playwright", "test", "--project=selftest", "--reporter=json"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "inherit"],
    env: { ...process.env, PLAYWRIGHT_JSON_OUTPUT_NAME: "test-results/selftest.json" },
  });
  console.error("selftest: テストが失敗しなかった");
  process.exit(1);
} catch {
  report = JSON.parse(readFileSync("test-results/selftest.json", "utf8"));
}

const results = [];
const walk = (suite) => {
  for (const s of suite.suites ?? []) walk(s);
  for (const spec of suite.specs ?? []) for (const t of spec.tests) results.push(...t.results);
};
for (const s of report.suites) walk(s);
const failed = results.find((r) => r.status === "failed");
if (!failed) {
  console.error("selftest: 失敗した結果がない");
  process.exit(1);
}
const names = failed.attachments.map((a) => a.name);
for (const want of ["logs.jsonl", "timeline.txt", "errors.txt"]) {
  if (!names.includes(want)) {
    console.error(`selftest: ${want} が添付されていない（${names.join(", ")}）`);
    process.exit(1);
  }
}
const body = (name) => {
  const a = failed.attachments.find((x) => x.name === name);
  return a.body ? Buffer.from(a.body, "base64").toString("utf8") : readFileSync(a.path, "utf8");
};
const lines = body("logs.jsonl").split("\n").filter(Boolean).map((l) => JSON.parse(l));
const corr = body("timeline.txt").split("\n")[0].match(/correlation_id=(\S+)/)?.[1];
if (lines.length < 2 || !corr || lines.some((l) => l.correlation_id !== corr)) {
  console.error(`selftest: logs.jsonl の行（${lines.length} 行）がそのテストの相関 ID（${corr}）だけを持っていない`);
  process.exit(1);
}
console.log(`selftest: OK（${lines.length} 行、相関 ID ${corr}）`);
