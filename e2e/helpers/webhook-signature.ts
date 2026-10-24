// 事業者 Webhook の署名の検証（integration §4.3）。Go の webhooksig とは独立に、仕様から書いた実装（WS-7）。
//   SP-Signature: t=<UNIX 秒>,v1=<hex>[,v1=<hex>]
//   署名対象 "<t>." + body のバイト列、HMAC-SHA256、鍵は whsec_ 以降を base64url デコードしたバイト列
import { createHmac, timingSafeEqual } from "node:crypto";
import type { SinkRecord } from "../fixtures/sink.ts";

export function parseSignature(header: string): { t: number; v1: string[] } {
  let t = NaN;
  const v1: string[] = [];
  for (const part of header.split(",")) {
    const [k, v] = part.split("=", 2);
    if (k === "t") t = Number(v);
    if (k === "v1" && v) v1.push(v);
  }
  return { t, v1 };
}

export function sign(secret: string, t: number, body: Buffer): string {
  const key = Buffer.from(secret.replace(/^whsec_/, ""), "base64url");
  return createHmac("sha256", key).update(`${t}.`).update(body).digest("hex");
}

/** sink の記録が secret で署名されているか（どれか 1 つの v1 が一致すればよい）。 */
export function verifySignature(rec: SinkRecord, secret: string, opts: { toleranceSec?: number; now?: number } = {}): boolean {
  const header = rec.headers["Sp-Signature"]?.[0] ?? rec.headers["SP-Signature"]?.[0];
  if (!header) return false;
  const { t, v1 } = parseSignature(header);
  if (!Number.isFinite(t)) return false;
  if (opts.toleranceSec !== undefined) {
    const now = opts.now ?? Math.floor(Date.now() / 1000);
    if (Math.abs(now - t) > opts.toleranceSec) return false;
  }
  const expected = Buffer.from(sign(secret, t, Buffer.from(rec.body_base64, "base64")), "hex");
  return v1.some((h) => {
    const got = Buffer.from(h, "hex");
    return got.length === expected.length && timingSafeEqual(got, expected);
  });
}
