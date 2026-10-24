// tokens fixture：開発用 JWT（POST /v1/dev/tokens）。sub・email は省略するとテストごとに一意な値。
import { randomUUID } from "node:crypto";
import { expect } from "@playwright/test";
import type { Api } from "./api.ts";

export type Pool = "consumer" | "staff" | "operator";

export interface IssueOptions {
  pool: Pool;
  sub?: string;
  email?: string;
  emailVerified?: boolean;
  name?: string;
  groups?: string[];
  ttl?: number;
}

export interface Tokens {
  issue(o: IssueOptions): Promise<{ token: string; sub: string; email: string }>;
  operator(): Promise<string>;
}

export function createTokens(api: Api, testTag: string): Tokens {
  const issue = async (o: IssueOptions) => {
    const sub = o.sub ?? `${o.pool}-${testTag}-${randomUUID().slice(0, 8)}`;
    const email = o.email ?? `${sub}@example.com`;
    const res = await api.dev.issueToken({
      pool: o.pool,
      sub,
      email,
      email_verified: o.emailVerified ?? true,
      name: o.name,
      groups: o.groups ?? (o.pool === "operator" ? ["operators"] : undefined),
      ttl_seconds: o.ttl,
    });
    expect(res.status, JSON.stringify(res.body)).toBe(200);
    return { token: (res.body as { token: string }).token, sub, email };
  };
  return {
    issue,
    operator: async () => (await issue({ pool: "operator" })).token,
  };
}
