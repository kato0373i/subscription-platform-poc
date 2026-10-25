// ダッシュボードの集計（console-dashboard）の確認。集計は worker-projection が非同期に作るので、期待する値になるまで待つ。
import type { ConsoleReportingComponents } from "@sp/api-client";
import type { Api } from "../fixtures/api.ts";
import type { Wait } from "../fixtures/wait.ts";
import type { TenantWorld } from "../fixtures/world.ts";

export type Overview = ConsoleReportingComponents["schemas"]["Overview"];

export async function overview(api: Api, tenant: TenantWorld, month?: string, token = tenant.ownerToken) {
  return api.call<Overview>("GET", "/v1/console/reports/overview", { token, tenantId: tenant.id, query: { month } });
}

/** 集計が cond を満たすまで待つ。 */
export async function untilOverview(api: Api, wait: Wait, tenant: TenantWorld, cond: (o: Overview) => boolean, message: string, month?: string): Promise<Overview> {
  let last: Overview | undefined;
  try {
    return await wait.until(
      async () => {
        const r = await overview(api, tenant, month);
        last = r.body;
        return r.status === 200 && cond(r.body) ? r.body : undefined;
      },
      { message },
    );
  } catch (e) {
    throw new Error(`${(e as Error).message}\n最後の集計: ${JSON.stringify(last?.kgi)} ${JSON.stringify(last?.kpis.current)}`);
  }
}

/** YYYY-MM-DD の月（YYYY-MM）。 */
export const monthOf = (date: string) => date.slice(0, 7);
