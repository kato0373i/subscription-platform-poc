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
