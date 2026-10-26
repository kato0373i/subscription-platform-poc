// カードのトークン化（consumer-mypage §7）。申込ページと同じく、有効な手段を調べてトークン化の JS を読み込む。
// カード番号はブラウザの中でだけトークンにし、サーバーにはトークンだけを送る（NFR-003）。本番は fincode の JS に差し替える。
export interface Tokenizer {
  tokenize(card: { number: string; exp: string }): { token: string; last4: string };
}

declare global {
  interface Window {
    SPStubTokenizer?: Tokenizer;
  }
}

let loading: Promise<{ tokenizer: Tokenizer; provider: string }> | null = null;

export function loadTokenizer(): Promise<{ tokenizer: Tokenizer; provider: string }> {
  loading ??= (async () => {
    const res = await fetch("/v1/checkout/payment-config", { cache: "no-store" });
    if (!res.ok) throw new Error(`payment-config: ${res.status}`);
    const cfg = (await res.json()) as { methods: { method_type: string; provider: string }[] };
    const card = cfg.methods.find((m) => m.method_type === "card");
    if (!card) throw new Error("no card method");
    if (card.provider !== "stub") throw new Error(`unsupported provider: ${card.provider}`); // fincode の JS は将来（要件 §9 #3）
    await new Promise<void>((resolve, reject) => {
      const s = document.createElement("script");
      s.src = "/checkout/stub-tokenizer.js";
      s.onload = () => resolve();
      s.onerror = () => reject(new Error("tokenizer load failed"));
      document.head.appendChild(s);
    });
    if (!window.SPStubTokenizer) throw new Error("tokenizer missing");
    return { tokenizer: window.SPStubTokenizer, provider: card.provider };
  })();
  loading.catch(() => (loading = null));
  return loading;
}
