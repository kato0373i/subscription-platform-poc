// 表示の形式（NFR-008）：日時は JST、金額は円・3 桁区切り。
const dt = new Intl.DateTimeFormat("ja-JP", { timeZone: "Asia/Tokyo", year: "numeric", month: "long", day: "numeric", hour: "2-digit", minute: "2-digit" });
const d = new Intl.DateTimeFormat("ja-JP", { timeZone: "Asia/Tokyo", year: "numeric", month: "long", day: "numeric" });

export function yen(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  return `${n.toLocaleString("ja-JP")}円`;
}

export function dateTime(s: string | null | undefined): string {
  if (!s) return "—";
  return dt.format(new Date(s));
}

/** YYYY-MM-DD（暦日）を「2026年10月26日」にする。 */
export function date(s: string | null | undefined): string {
  if (!s) return "—";
  return d.format(new Date(s.length === 10 ? `${s}T00:00:00+09:00` : s));
}

/** 符号付きの円（+1,980円・−20円）。 */
export function signedYen(n: number): string {
  if (n === 0) return "0円";
  return `${n > 0 ? "+" : "−"}${Math.abs(n).toLocaleString("ja-JP")}円`;
}

/** 率（0.667 → 66.7%）。null は「—」。 */
export function percent(v: number | null | undefined, digits = 1): string {
  if (v === null || v === undefined) return "—";
  return `${(v * 100).toFixed(digits)}%`;
}

/** 金額の軸の短い表記（12,000 → 1.2万）。 */
export function compactYen(n: number): string {
  if (Math.abs(n) >= 100_000_000) return `${(n / 100_000_000).toFixed(1).replace(/\.0$/, "")}億`;
  if (Math.abs(n) >= 10_000) return `${(n / 10_000).toFixed(1).replace(/\.0$/, "")}万`;
  return n.toLocaleString("ja-JP");
}
