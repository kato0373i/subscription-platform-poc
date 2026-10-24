// mail fixture：メールの確認は既定で DB（integration.email_notifications）の行で行う（integration §11.3 の B）。
// MiniStack の SES から送ったメールを取り出す API（GET /_ministack/ses/messages）も使える（§11.3 の A。verify:ministack で確認済み）。
import type { APIRequestContext } from "@playwright/test";
import type { Db } from "./db.ts";
import type { Wait } from "./wait.ts";

export interface EmailNotification {
  id: string;
  status: string;
  dedupe_key: string;
  template_key: string;
  variables: Record<string, unknown>;
  subject: string | null;
  provider_message_id: string | null;
}

export interface SesMessage {
  MessageId: string;
  Source: string;
  To: string[];
  Subject: string;
  BodyText: string;
}

export interface Mail {
  notifications(f: { dedupeKey?: string; templateKey?: string; customerAccountId?: string }): Promise<EmailNotification[]>;
  waitForSent(dedupeKey: string): Promise<EmailNotification>;
  sesMessages(): Promise<SesMessage[]>;
}

export function createMail(db: Db, wait: Wait, request: APIRequestContext): Mail {
  const notifications: Mail["notifications"] = (f) =>
    db.query<EmailNotification>(
      `SELECT id, status, dedupe_key, template_key, variables, subject, provider_message_id
         FROM integration.email_notifications
        WHERE ($1::text IS NULL OR dedupe_key = $1) AND ($2::text IS NULL OR template_key = $2)
          AND ($3::uuid IS NULL OR customer_account_id = $3::uuid)
        ORDER BY created_at`,
      [f.dedupeKey ?? null, f.templateKey ?? null, f.customerAccountId ?? null],
    );
  return {
    notifications,
    waitForSent: (dedupeKey) =>
      wait.until(async () => (await notifications({ dedupeKey })).find((n) => n.status === "sent"), { message: `メール ${dedupeKey} が sent になる` }),
    async sesMessages() {
      const endpoint = process.env.AWS_ENDPOINT_URL ?? "http://localhost:4566";
      const res = await request.get(`${endpoint}/_ministack/ses/messages`);
      const body = (await res.json()) as { messages: Record<string, SesMessage[]> };
      return Object.values(body.messages).flat();
    },
  };
}
