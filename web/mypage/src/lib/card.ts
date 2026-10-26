import type { PaymentMethod } from "../api/types.ts";

export function cardLabel(pm: PaymentMethod): string {
  const d = pm.display;
  return `${d.brand.toUpperCase()} •••• ${d.last4}（${d.exp_month}/${String(d.exp_year).slice(-2)}）`;
}
