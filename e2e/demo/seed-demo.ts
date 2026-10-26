// デモデータの投入（ローカル専用。task demo:seed）。まっさらな環境（task reset → up → dev:container）で流す。
// dev clock を 1 日ずつ進めながら、約 12 か月分の事業の動き（申込・トライアル・プラン変更・支払い失敗と回復・解約）を本物の API で作る。
// dev clock は戻せないので、履歴は「今日」から 12 か月先までになり、最後の日がコンソールの「今日」になる。
// 事業者コンソールのログイン用の Cognito アカウント（owner・admin・viewer）・マイページの契約者・運営者コンソールの運営者を作り、ログイン情報を .local/demo-accounts.md に書く。
import { randomUUID } from "node:crypto";
import { mkdirSync, writeFileSync } from "node:fs";
import pg from "pg";

const API = process.env.API_BASE_URL ?? "http://localhost:8080";
const AWS = process.env.AWS_ENDPOINT_URL ?? "http://localhost:4566";
const POOL = process.env.COGNITO_STAFF_USER_POOL_ID ?? "";
const CONSUMER_POOL = process.env.COGNITO_CONSUMER_USER_POOL_ID ?? "";
const OPERATOR_POOL = process.env.COGNITO_OPERATOR_USER_POOL_ID ?? "";
const CONSOLE = process.env.CONSOLE_BASE_URL ?? "http://localhost:8090";
const MONTHS = Number(process.env.DEMO_MONTHS ?? "12");
// DATE（1082）は文字列のまま受け取る（Date にするとローカル時刻で日付がずれる）
pg.types.setTypeParser(1082, (v: string) => v);
const db = new pg.Client({ connectionString: process.env.E2E_READER_URL });

// ---- 乱数（毎回同じデータになるよう種を固定） ----
let seed = 20261026;
const rnd = () => ((seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648);
const chance = (p: number) => rnd() < p;
const pick = <T>(xs: T[]) => xs[Math.floor(rnd() * xs.length)]!;

// ---- API ----
interface Res<T = any> { status: number; body: T }
async function call<T = any>(method: string, path: string, o: { token?: string; tenantId?: string; body?: unknown; idem?: boolean; ifMatch?: string } = {}): Promise<Res<T>> {
  const h: Record<string, string> = { "Content-Type": "application/json", "X-Correlation-Id": `demo-seed-${randomUUID().slice(0, 8)}` };
  if (o.token) h.Authorization = `Bearer ${o.token}`;
  if (o.tenantId) h["X-Tenant-Id"] = o.tenantId;
  if (o.idem !== false && method === "POST") h["Idempotency-Key"] = randomUUID();
  if (o.ifMatch) h["If-Match"] = o.ifMatch;
  const r = await fetch(API + path, { method, headers: h, body: o.body === undefined ? undefined : JSON.stringify(o.body) });
  const text = await r.text();
  return { status: r.status, body: text ? JSON.parse(text) : undefined };
}
async function must<T = any>(method: string, path: string, o: Parameters<typeof call>[2] = {}, ok = [200, 201, 202, 204]): Promise<T> {
  const r = await call<T>(method, path, o);
  if (!ok.includes(r.status)) throw new Error(`${method} ${path}: ${r.status} ${JSON.stringify(r.body)}`);
  return r.body;
}
async function token(pool: "consumer" | "staff" | "operator", sub: string, email: string, name?: string): Promise<string> {
  const b = await must<{ token: string }>("POST", "/v1/dev/tokens", {
    body: { pool, sub, email, email_verified: true, name, groups: pool === "operator" ? ["operators"] : undefined },
  });
  return b.token;
}

// ---- dev clock とジョブ ----
async function now(): Promise<{ now: string; today: string }> {
  return must("GET", "/v1/dev/clock");
}
async function setTo(date: string, hhmm: string) {
  const r = await call<{ effective_after?: string }>("POST", "/v1/dev/clock", { body: { to: `${date}T${hhmm}:00+09:00` } });
  if (r.status !== 200) return; // 同じ時刻・過去への設定は無視
  const ms = r.body.effective_after ? new Date(r.body.effective_after).getTime() - Date.now() : 0;
  if (ms > 0) await new Promise((res) => setTimeout(res, ms + 50));
}
async function runJob(job: string, asOf?: string) {
  const run = await must<{ status: string; error?: string }>("POST", `/v1/dev/jobs/${job}/run`, { body: { as_of: asOf, force: true, wait_seconds: 120 } });
  if (run.status !== "succeeded") throw new Error(`${job}: ${run.status} ${run.error ?? ""}`);
}
const addDays = (d: string, n: number) => {
  const x = new Date(`${d}T00:00:00Z`);
  x.setUTCDate(x.getUTCDate() + n);
  return x.toISOString().slice(0, 10);
};
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
async function one(sql: string, args: unknown[]): Promise<any> {
  return (await db.query(sql, args)).rows[0];
}

/** その日の処理（更新・回収・申込の後の非同期の処理）が終わるまで待つ。 */
async function settle(tenantIds: string[], date: string) {
  const deadline = Date.now() + 180_000;
  let stable = 0;
  while (Date.now() < deadline) {
    const at = (await now()).now;
    const r = await one(
      `SELECT
         (SELECT count(*) FROM orchestration.billing_cycle_processes WHERE tenant_id = ANY($1::uuid[]) AND outcome IS NULL) AS open,
         (SELECT count(*) FROM subscription.subscriptions WHERE tenant_id = ANY($1::uuid[]) AND status IN ('trialing','active') AND next_renewal_date <= $2) AS due,
         (SELECT count(*) FROM billing.invoices WHERE tenant_id = ANY($1::uuid[]) AND status = 'open' AND collection_mode = 'scheduled' AND next_collection_at <= $3) AS collect`,
      [tenantIds, date, at],
    );
    if (Number(r.open) === 0 && Number(r.due) === 0 && Number(r.collect) === 0) {
      if (++stable >= 2) return;
    } else stable = 0;
    await sleep(400);
  }
  throw new Error(`settle: ${date} の処理が終わりません`);
}

// ---- Cognito（MiniStack。署名は検証されない） ----
async function cognito<T = unknown>(action: string, body: object): Promise<T> {
  const r = await fetch(AWS, {
    method: "POST",
    headers: {
      "Content-Type": "application/x-amz-json-1.1",
      "X-Amz-Target": `AWSCognitoIdentityProviderService.${action}`,
      Authorization: "AWS4-HMAC-SHA256 Credential=test/20260101/ap-northeast-1/cognito-idp/aws4_request, SignedHeaders=host, Signature=demo",
    },
    body: JSON.stringify(body),
  });
  if (!r.ok) throw new Error(`cognito ${action}: ${r.status} ${await r.text()}`);
  const text = await r.text();
  return (text ? JSON.parse(text) : {}) as T;
}
function password(): string {
  return `Demo-${randomUUID().slice(0, 4)}${randomUUID().slice(0, 4)}Aa1!`;
}

// ---- 名前 ----
const family = ["佐藤", "鈴木", "高橋", "田中", "伊藤", "渡辺", "山本", "中村", "小林", "加藤", "吉田", "山田", "佐々木", "山口", "松本", "井上", "木村", "林", "清水", "斎藤"];
const given = ["陽菜", "蓮", "結衣", "湊", "葵", "大翔", "凛", "悠真", "美咲", "颯太", "さくら", "陸", "芽依", "樹", "七海", "翔", "結菜", "健太", "彩花", "拓海"];
const roman: Record<string, string> = { 佐藤: "sato", 鈴木: "suzuki", 高橋: "takahashi", 田中: "tanaka", 伊藤: "ito", 渡辺: "watanabe", 山本: "yamamoto", 中村: "nakamura", 小林: "kobayashi", 加藤: "kato", 吉田: "yoshida", 山田: "yamada", 佐々木: "sasaki", 山口: "yamaguchi", 松本: "matsumoto", 井上: "inoue", 木村: "kimura", 林: "hayashi", 清水: "shimizu", 斎藤: "saito" };

// ---- 契約の追跡 ----
interface Sub {
  id: string;
  sub: string;
  email: string;
  name: string;
  pm: string;
  plan: string;
  trial: boolean;
  cancelOn?: string; // トライアル中の解約の予約をする日
  failFlagged: boolean;
}

async function main() {
  await db.connect();
  const start = (await now()).today;
  const end = addDays(start, MONTHS * 30);
  console.log(`demo: ${start} から ${end} まで（${MONTHS} か月）`);
  await setTo(start, "09:00");

  // 事業者・商品・プラン
  // 開発用 JWT の有効期限は業務時刻で 1 時間なので、使うたびに発行し直す
  const opTok = () => token("operator", "demo-operator", "operator@demo.example.com");
  let op = await opTok();
  const tenant = await must<{ id: string }>("POST", "/v1/ops/tenants", { token: op, body: { name: "ミライ動画配信" } });
  const T = tenant.id;
  await must("POST", `/v1/ops/tenants/${T}/approve`, { token: op, body: { fincode_shop_id: `shop_demo${T.replaceAll("-", "").slice(0, 12)}` } });
  const ownerSub = "demo-api-owner";
  const ownerTok = () => token("staff", ownerSub, "api-owner@demo.example.com", "API 用オーナー");
  let owner = await ownerTok();
  await must("POST", `/v1/ops/tenants/${T}/staff`, { token: op, body: { subject: ownerSub, email: "api-owner@demo.example.com", display_name: "API 用オーナー", role: "owner" } });
  const c = async (method: string, path: string, body?: unknown, ifMatch?: string) => must(method, path, { token: (owner = await ownerTok()), tenantId: T, body, ifMatch });

  await c("PUT", "/v1/console/dunning-policy", { retry_offsets_days: [1, 3] });
  const video = await c("POST", "/v1/console/products", { name: "動画見放題", description: "映画・ドラマ・アニメが見放題の定額サービス" });
  const english = await c("POST", "/v1/console/products", { name: "オンライン英会話", description: "毎日 25 分のマンツーマンレッスン" });
  const mk = async (product_id: string, name: string, amount: number, interval: "month" | "year", trial_days: number, description: string) => {
    const p = await c("POST", "/v1/console/plans", { product_id, name, description, billing_interval: interval, trial_days, pricing_scheme: "fixed", amount });
    await c("POST", `/v1/console/plans/${p.id}/publish`);
    return p.id as string;
  };
  const basic = await mk(video.id, "ベーシック", 980, "month", 14, "HD 画質・1 台で視聴");
  const pro = await mk(video.id, "プロ", 1980, "month", 0, "4K 画質・4 台まで同時視聴");
  const proYear = await mk(video.id, "プロ（年払い）", 19800, "year", 0, "プロを年払いで 2 か月分お得に");
  const standard = await mk(english.id, "スタンダード", 4980, "month", 7, "毎日 1 レッスン");
  const draft = await c("POST", "/v1/console/plans", { product_id: english.id, name: "プレミアム（準備中）", description: "ネイティブ講師", billing_interval: "month", trial_days: 0, pricing_scheme: "fixed", amount: 9800 });
  void draft;
  await c("POST", "/v1/console/coupons", { code: "WELCOME50", name: "初月 50% オフ", discount: { type: "percent", value: 50, duration_cycles: 1 }, applies_to: "all_plans", plan_ids: [] });
  await c("POST", "/v1/console/coupons", { code: "ENGLISH1000", name: "英会話 1,000 円引き（3 か月）", discount: { type: "amount", value: 1000, duration_cycles: 3 }, applies_to: "selected_plans", plan_ids: [standard] });
  await c("POST", "/v1/console/webhook-endpoints", { url: "http://webhook-sink:8080/demo", description: "会員システム（デモ）", event_types: ["subscription.started", "subscription.renewed", "subscription.canceled", "invoice.paid", "invoice.payment_failed"] });
  await c("POST", "/v1/console/api-keys", { name: "会員システム（本番）" });

  // 申込の予定：月ごとに少しずつ増える
  const signups = new Map<string, number>();
  for (let m = 0; m < MONTHS; m++) {
    const n = Math.max(3, Math.round(7 + m * 1.6 + (rnd() * 4 - 2)));
    for (let i = 0; i < n; i++) {
      const d = addDays(start, m * 30 + Math.floor(rnd() * 30));
      if (d < end) signups.set(d, (signups.get(d) ?? 0) + 1);
    }
  }
  const subs = new Map<string, Sub>();
  let seq = 0;

  // マイページのデモの契約者（Cognito の契約者のプールのアカウント）。契約者は Cognito の sub で決まるので、同じ sub の開発用 JWT で申し込む。
  // 2 つの事業者と契約し、1 件は解約済みにする。ランダムな支払い失敗・解約の対象にはしない
  const books = await must<{ id: string }>("POST", "/v1/ops/tenants", { token: op, body: { name: "ブックス定期便" } });
  await must("POST", `/v1/ops/tenants/${books.id}/approve`, { token: op, body: { fincode_shop_id: `shop_demo${books.id.replaceAll("-", "").slice(0, 12)}` } });
  await must("POST", `/v1/ops/tenants/${books.id}/staff`, { token: op, body: { subject: ownerSub, email: "api-owner@demo.example.com", display_name: "API 用オーナー", role: "owner" } });
  const bc = async (method: string, path: string, body?: unknown) => must(method, path, { token: (owner = await ownerTok()), tenantId: books.id, body });
  const bookProduct = await bc("POST", "/v1/console/products", { name: "毎月の本", description: "選書のプロが毎月 2 冊をお届け" });
  const bookPlan = await bc("POST", "/v1/console/plans", { product_id: bookProduct.id, name: "2 冊コース", description: "", billing_interval: "month", trial_days: 0, pricing_scheme: "fixed", amount: 2980 });
  await bc("POST", `/v1/console/plans/${bookPlan.id}/publish`);
  const demoConsumer = { email: "customer@demo.example.com", name: "田中 美咲", password: `demo${randomUUID().slice(0, 6)}1` };
  const created = await cognito<{ User: { Attributes: { Name: string; Value: string }[] } }>("AdminCreateUser", {
    UserPoolId: CONSUMER_POOL, Username: demoConsumer.email, MessageAction: "SUPPRESS",
    UserAttributes: [{ Name: "email", Value: demoConsumer.email }, { Name: "email_verified", Value: "true" }, { Name: "name", Value: demoConsumer.name }],
  });
  const demoSub = created.User.Attributes.find((a) => a.Name === "sub")!.Value;
  await cognito("AdminSetUserPassword", { UserPoolId: CONSUMER_POOL, Username: demoSub, Password: demoConsumer.password, Permanent: true });
  const demoTok = () => token("consumer", demoSub, demoConsumer.email, demoConsumer.name);
  await must("GET", "/v1/me", { token: await demoTok() });
  const demoPm = await must<{ id: string }>("POST", "/v1/me/payment-methods", {
    token: await demoTok(), body: { method_type: "card", provider: "stub", token: `tok_stub_ok_4242_1230_${randomUUID().replaceAll("-", "").slice(0, 8)}` },
  });
  const demoSubs: string[] = [];
  for (const [tid, pid] of [[T, pro], [books.id, bookPlan.id as string], [T, standard]] as const) {
    const r = await must<{ subscription: { id: string } }>("POST", "/v1/me/subscriptions", { token: await demoTok(), body: { tenant_id: tid, plan_id: pid, payment_method_id: demoPm.id } });
    demoSubs.push(r.subscription.id);
  }
  const demoCancelOn = addDays(start, 20); // 英会話はトライアル中に解約する（解約済みの表示の確認用）

  const signup = async () => {
    const f = pick(family), g = pick(given);
    const sub = `demo-c-${++seq}`;
    const email = `${roman[f]}.${seq}@demo-customer.example.com`;
    const ct = await token("consumer", sub, email, `${f} ${g}`);
    await must("GET", "/v1/me", { token: ct });
    const pm = await must<{ id: string }>("POST", "/v1/me/payment-methods", {
      token: ct, body: { method_type: "card", provider: "stub", token: `tok_stub_ok_4242_1299_${randomUUID().replaceAll("-", "").slice(0, 8)}` },
    });
    const r = rnd();
    const plan = r < 0.45 ? basic : r < 0.72 ? pro : r < 0.82 ? proYear : standard;
    const coupon = plan === standard ? (chance(0.3) ? "ENGLISH1000" : undefined) : chance(0.18) ? "WELCOME50" : undefined;
    const res = await call<{ subscription: { id: string; status: string } }>("POST", "/v1/me/subscriptions", {
      token: ct, body: { tenant_id: T, plan_id: plan, payment_method_id: pm.id, coupon_code: coupon },
    });
    if (res.status !== 201) return; // 決済の失敗などは数えない
    const s = res.body.subscription;
    const trial = s.status === "trialing";
    const today = (await now()).today;
    subs.set(s.id, { id: s.id, sub, email, name: `${f} ${g}`, pm: pm.id, plan, trial, failFlagged: false, cancelOn: trial && chance(0.28) ? addDays(today, 2 + Math.floor(rnd() * 9)) : undefined });
  };

  // 毎日の処理：更新日を迎える契約・回収の時刻を迎えた請求・申込・予約のある日だけを順に訪ねる
  let day = start;
  let visited = 0;
  let demoCanceled = false;
  while (day < end) {
    // 1) 00:30 更新の前に、今日更新する契約のいくつかの支払いを失敗させる
    await setTo(day, "00:30");
    const due = await db.query(`SELECT id FROM subscription.subscriptions WHERE tenant_id = $1 AND status IN ('trialing','active') AND next_renewal_date = $2`, [T, day]);
    for (const { id } of due.rows) {
      const s = subs.get(id);
      if (!s || s.failFlagged) continue;
      if (chance(0.06)) {
        await must("PUT", `/v1/dev/payments/stub/payment-methods/${s.pm}/behavior`, { body: { scenario: "decline", remaining: 1 } }); // 再請求で回復
        s.failFlagged = true;
      } else if (chance(0.04)) {
        await must("PUT", `/v1/dev/payments/stub/payment-methods/${s.pm}/behavior`, { body: { scenario: "decline", remaining: 99 } }); // 支払い失敗で解約
        s.failFlagged = true;
      }
    }
    await runJob("orchestration-renewal-dispatch", day);
    await settle([T, books.id], day);
    // 2) 10:00 再請求
    await setTo(day, "10:00");
    await runJob("billing-collection-due");
    await settle([T, books.id], day);
    // 3) 11:00 契約者の操作（申込・トライアル中の解約・更新した契約の解約やプラン変更の予約）
    await setTo(day, "11:00");
    for (let i = 0; i < (signups.get(day) ?? 0); i++) await signup();
    if (day === demoCancelOn || (day > demoCancelOn && !demoCanceled)) {
      await call("PUT", `/v1/me/subscriptions/${demoSubs[2]}/scheduled-cancellation`, { token: await demoTok(), body: {} });
      demoCanceled = true;
    }
    for (const s of subs.values()) {
      if (s.cancelOn === day) {
        await call("PUT", `/v1/me/subscriptions/${s.id}/scheduled-cancellation`, { token: await token("consumer", s.sub, s.email, s.name), body: {} });
        s.cancelOn = undefined;
      }
    }
    for (const { id } of due.rows) {
      const s = subs.get(id);
      if (!s) continue;
      const cur = await one(`SELECT status, plan_id, cancel_at_period_end, pending_plan_id FROM subscription.subscriptions WHERE id = $1`, [id]);
      if (cur?.status !== "active" || cur.cancel_at_period_end || cur.pending_plan_id) continue;
      const ct = await token("consumer", s.sub, s.email, s.name);
      if (chance(0.045)) {
        await call("PUT", `/v1/me/subscriptions/${id}/scheduled-cancellation`, { token: ct, body: {} });
      } else if (cur.plan_id === basic && chance(0.07)) {
        await call("PUT", `/v1/me/subscriptions/${id}/scheduled-plan-change`, { token: ct, body: { plan_id: pro } });
      } else if (cur.plan_id === pro && chance(0.03)) {
        await call("PUT", `/v1/me/subscriptions/${id}/scheduled-plan-change`, { token: ct, body: { plan_id: basic } });
      }
    }
    visited++;
    if (visited % 10 === 0) console.log(`demo: ${day}（契約 ${subs.size} 件）`);

    // 次に訪ねる日
    const next = await one(
      `SELECT least(
         (SELECT min(next_renewal_date) FROM subscription.subscriptions WHERE tenant_id = ANY($1::uuid[]) AND status IN ('trialing','active') AND next_renewal_date > $2),
         (SELECT min((next_collection_at AT TIME ZONE 'Asia/Tokyo')::date) FROM billing.invoices WHERE tenant_id = ANY($1::uuid[]) AND status = 'open' AND collection_mode = 'scheduled' AND (next_collection_at AT TIME ZONE 'Asia/Tokyo')::date > $2)
       ) AS d`,
      [[T, books.id], day],
    );
    const candidates: string[] = [next?.d ?? end];
    for (const d of signups.keys()) if (d > day) candidates.push(d);
    for (const s of subs.values()) if (s.cancelOn && s.cancelOn > day) candidates.push(s.cancelOn);
    if (!demoCanceled && demoCancelOn > day) candidates.push(demoCancelOn);
    day = candidates.sort()[0]!;
  }
  await setTo(end, "12:00");

  // 目標（当月）と、2 つ目の事業者（審査中。事業者の切り替えの確認用）
  const month = end.slice(0, 7);
  owner = await ownerTok();
  op = await opTok();
  await must("PUT", `/v1/console/mrr-goals/${month}`, { token: owner, tenantId: T, body: { amount: 250000 } });
  const second = await must<{ id: string }>("POST", "/v1/ops/tenants", { token: op, body: { name: "ミライ書店（審査中）" } });
  await must("POST", `/v1/ops/tenants/${second.id}/staff`, { token: op, body: { subject: ownerSub, email: "api-owner@demo.example.com", display_name: "API 用オーナー", role: "owner" } });

  // コンソールのログイン用の Cognito アカウント
  const accounts = [
    { role: "owner", label: "オーナー", email: "owner@demo.example.com", name: "山田 太郎" },
    { role: "admin", label: "管理者", email: "admin@demo.example.com", name: "佐藤 花子" },
    { role: "viewer", label: "閲覧者", email: "viewer@demo.example.com", name: "鈴木 一郎" },
  ];
  const lines: string[] = [];
  for (const a of accounts) {
    const pw = password();
    const st = await must<{ subject: string }>("POST", "/v1/console/staff", { token: owner, tenantId: T, body: { email: a.email, display_name: a.name, role: a.role } });
    await cognito("AdminSetUserPassword", { UserPoolId: POOL, Username: st.subject, Password: pw, Permanent: true });
    lines.push(`| ${a.label} | \`${a.email}\` | \`${pw}\` |`);
  }
  // 運営者コンソールの運営者（本番と同じく管理者が作り、グループ operators に入れる。operator-console §5.3）
  const operator = { email: "operator@demo.example.com", password: password() };
  await cognito("AdminCreateUser", {
    UserPoolId: OPERATOR_POOL, Username: operator.email, MessageAction: "SUPPRESS",
    UserAttributes: [{ Name: "email", Value: operator.email }, { Name: "email_verified", Value: "true" }],
  });
  await cognito("AdminSetUserPassword", { UserPoolId: OPERATOR_POOL, Username: operator.email, Password: operator.password, Permanent: true });
  await cognito("AdminAddUserToGroup", { UserPoolId: OPERATOR_POOL, Username: operator.email, GroupName: "operators" });
  // オーナーは 2 つ目の事業者にも所属する（既存のアカウントなので、すぐに有効になる）
  await must("POST", "/v1/console/staff", { token: owner, tenantId: second.id, body: { email: accounts[0]!.email, display_name: accounts[0]!.name, role: "owner" } });

  const stat = await one(`SELECT count(*) FILTER (WHERE status IN ('active','past_due')) AS paying, count(*) FILTER (WHERE status = 'trialing') AS trialing, count(*) FILTER (WHERE status = 'canceled') AS canceled, count(*) AS total FROM subscription.subscriptions WHERE tenant_id = $1`, [T]);
  mkdirSync("../.local", { recursive: true });
  writeFileSync(
    "../.local/demo-accounts.md",
    [
      "# デモ環境のログイン情報（ローカル専用・MiniStack の Cognito）",
      "",
      `- コンソール：${CONSOLE}/console/login`,
      `- 事業者：ミライ動画配信（ID \`${T}\`）。オーナーは「ミライ書店（審査中）」にも所属`,
      `- データ：${start} 〜 ${end}（dev clock は戻せないので、今日から ${MONTHS} か月先まで進めて作った。コンソールの「今日」は ${end}）`,
      `- 契約：合計 ${stat.total} 件（有料 ${stat.paying}・トライアル中 ${stat.trialing}・解約済み ${stat.canceled}）`,
      "",
      "| ロール | メールアドレス | パスワード |",
      "|---|---|---|",
      ...lines,
      "",
      "## マイページ（契約者）",
      "",
      `- マイページ：${CONSOLE}/mypage/login`,
      "- 「ミライ動画配信」のプロ・「ブックス定期便」の 2 冊コースを利用中、オンライン英会話はトライアル中に解約済み",
      "",
      "| メールアドレス | パスワード |",
      "|---|---|",
      `| \`${demoConsumer.email}\` | \`${demoConsumer.password}\` |`,
      "",
      "## 運営者コンソール",
      "",
      `- 運営者コンソール：${CONSOLE}/ops/login`,
      "",
      "| メールアドレス | パスワード |",
      "|---|---|",
      `| \`${operator.email}\` | \`${operator.password}\` |`,
      "",
      "`task reset` で消える。作り直すときは `task reset` → `task up` → `task dev:container` → `task demo:seed`。",
      "",
    ].join("\n"),
  );
  console.log(`demo: 完了。契約 ${stat.total} 件。ログイン情報は .local/demo-accounts.md`);
  await db.end();
}

main().catch(async (e) => {
  console.error(e);
  await db.end().catch(() => undefined);
  process.exit(1);
});
