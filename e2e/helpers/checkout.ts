// 最小の申込ページ（payments.md §7.10・§11.3）をブラウザで操作する準備。カード番号が自社に届かないこと（NFR-007）の検査を含む。
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, type Page, type Request } from "@playwright/test";
import type { Db } from "../fixtures/db.ts";

/** stub-tokenizer.js のテストカード（payments.md §4.5）。 */
export const STUB_CARDS = {
  ok: "4242 4242 4242 4242",
  decline: "4000 0000 0000 0002",
  insufficient: "4000 0000 0000 9995",
} as const;

export interface RecordedRequest {
  url: string;
  origin: string;
  method: string;
  headers: Record<string, string>;
  body: string;
}

/** page.goto の前に呼ぶ。全リクエストの URL・ヘッダー・本文を記録する。 */
export function recordRequests(page: Page): RecordedRequest[] {
  const out: RecordedRequest[] = [];
  const pending: Promise<void>[] = [];
  page.on("request", (r: Request) => {
    pending.push(
      r.allHeaders().then((headers) => {
        out.push({ url: r.url(), origin: new URL(r.url()).origin, method: r.method(), headers, body: r.postDataBuffer()?.toString("utf8") ?? "" });
      }),
    );
  });
  Object.defineProperty(out, "settled", { value: () => Promise.all(pending) });
  return out;
}

export async function settled(reqs: RecordedRequest[]) {
  await (reqs as unknown as { settled: () => Promise<unknown> }).settled();
}

/** カード番号の正規化形（数字だけ・4 桁ごとの空白・ハイフン・数字だけの Base64）。 */
export function panForms(pan: string): string[] {
  const digits = pan.replace(/\D/g, "");
  const groups = digits.match(/.{1,4}/g)!;
  return [digits, groups.join(" "), groups.join("-"), Buffer.from(digits).toString("base64")];
}

/** 文字列の中のカード番号の正規化形（URL デコードした形・JSON の文字列値も対象）。 */
export function findPan(text: string, pan: string): string[] {
  let decoded = text;
  try {
    decoded = decodeURIComponent(text.replace(/\+/g, " "));
  } catch {
    /* デコードできない本文はそのまま */
  }
  const hay = `${text}\n${decoded}`;
  return panForms(pan).filter((f) => hay.includes(f));
}

/** 申込ページを開く。トークンはフラグメントで渡す（サーバーに送られない）。 */
export async function openCheckout(page: Page, query: string, token: string) {
  await page.goto(`/checkout?${query}#token=${encodeURIComponent(token)}`);
  await expect(page.locator("#result")).toHaveAttribute("data-status", "ready");
}

/** カード番号を 1 文字ずつ入力し、有効期限を入れて申し込む。 */
export async function fillCardAndSubmit(page: Page, pan: string, exp = "12/28") {
  await page.locator('input[autocomplete="cc-number"]').pressSequentially(pan);
  await page.locator('input[autocomplete="cc-exp"]').pressSequentially(exp);
  await page.locator("#submit").click();
}

/** 全アプリスキーマの文字列・JSON 列に数字だけのカード番号が現れないこと（§11.3 の 10）。 */
export async function expectPanNotInDb(db: Db, pan: string) {
  const digits = pan.replace(/\D/g, "");
  const cols = await db.query<{ table_schema: string; table_name: string; column_name: string }>(
    `SELECT c.table_schema, c.table_name, c.column_name FROM information_schema.columns c
       JOIN information_schema.tables t ON t.table_schema = c.table_schema AND t.table_name = c.table_name AND t.table_type = 'BASE TABLE'
      WHERE c.table_schema IN ('platform','tenant','customer','catalog','subscription','billing','payments','orchestration','integration','audit')
        AND c.data_type IN ('text','character varying','json','jsonb','bytea')`,
  );
  const byTable = new Map<string, string[]>();
  for (const c of cols) {
    const k = `"${c.table_schema}"."${c.table_name}"`;
    byTable.set(k, [...(byTable.get(k) ?? []), c.column_name]);
  }
  expect(byTable.size).toBeGreaterThan(20); // 列挙が働いていること
  const hits: string[] = [];
  for (const [table, columns] of byTable) {
    const where = columns.map((c) => `"${c}"::text LIKE $1`).join(" OR ");
    const n = await db.count(`SELECT count(*) FROM ${table} WHERE ${where}`, [`%${digits}%`]);
    if (n > 0) hits.push(table);
  }
  expect(hits, "カード番号が保存された表").toEqual([]);
}

/** 3 プロセスの JSON ログ（テスト開始以降の行）にカード番号の正規化形が現れないこと（§11.3 の 11）。 */
export async function expectPanNotInLogs(pan: string, since: Date) {
  const dir = process.env.LOG_DIR ?? "../.local/logs";
  const files = (await readdir(dir)).filter((f) => f.endsWith(".jsonl"));
  expect(files.length).toBeGreaterThanOrEqual(3);
  let scanned = 0;
  const hits: string[] = [];
  for (const f of files) {
    for (const line of (await readFile(join(dir, f), "utf8")).split("\n")) {
      const m = line.match(/"ts":"([^"]+)"/);
      if (!m || Date.parse(m[1]!) < since.getTime() - 1000) continue;
      scanned++;
      if (findPan(line, pan).length > 0) hits.push(`${f}: ${line.slice(0, 200)}`);
    }
  }
  expect(scanned, "検査したログの行").toBeGreaterThan(0);
  expect(hits).toEqual([]);
}
