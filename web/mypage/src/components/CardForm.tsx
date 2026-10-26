// カードの登録（FR-008）：番号と有効期限はブラウザの中でだけトークンにし、入力欄は form に入れず name を付けない（申込ページと同じ。NFR-003）。
import { Alert, Button, Group, Stack, TextInput } from "@mantine/core";
import { useRef, useState } from "react";
import { useApi } from "../api/hooks.ts";
import type { PaymentMethod } from "../api/types.ts";
import { loadTokenizer } from "../lib/tokenizer.ts";
import { errorMessage } from "../api/messages.ts";

export function CardForm({ submitLabel, onRegistered, onCancel }: { submitLabel: string; onRegistered: (pm: PaymentMethod) => Promise<void> | void; onCancel?: () => void }) {
  const api = useApi();
  const number = useRef<HTMLInputElement>(null);
  const exp = useRef<HTMLInputElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const { tokenizer, provider } = await loadTokenizer();
      let token: string;
      try {
        token = tokenizer.tokenize({ number: number.current?.value ?? "", exp: exp.current?.value ?? "" }).token;
      } catch {
        setError("カード番号と有効期限（MM/YY）を確認してください。");
        return;
      } finally {
        if (number.current) number.current.value = "";
        if (exp.current) exp.current.value = "";
      }
      const pm = await api.post<PaymentMethod>("/v1/me/payment-methods", { method_type: "card", provider, token });
      await onRegistered(pm);
    } catch (e) {
      setError(e instanceof Error && !("status" in e) ? "カードの入力の準備ができませんでした。時間をおいてお試しください。" : errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Stack gap="sm" data-testid="card-form">
      {error && (
        <Alert color="red" variant="light" role="alert">
          {error}
        </Alert>
      )}
      <TextInput ref={number} label="カード番号" inputMode="numeric" autoComplete="cc-number" placeholder="4242 4242 4242 4242" maxLength={23} />
      <TextInput ref={exp} label="有効期限（MM/YY）" inputMode="numeric" autoComplete="cc-exp" placeholder="12/28" maxLength={5} w={160} />
      <Group grow>
        {onCancel && (
          <Button variant="default" onClick={onCancel}>
            やめる
          </Button>
        )}
        <Button loading={busy} onClick={() => void submit()}>
          {submitLabel}
        </Button>
      </Group>
    </Stack>
  );
}
