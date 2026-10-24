// db fixture：読み取り専用の sp_e2e_reader で DB を読む（P-TST-3）。API で見えない不変条件の確認だけに使う。
import pg from "pg";

export interface Db {
  query<T = Record<string, unknown>>(sql: string, params?: unknown[]): Promise<T[]>;
  count(sql: string, params?: unknown[]): Promise<number>;
}

let pool: pg.Pool | undefined;

function getPool(): pg.Pool {
  if (!pool) {
    const url = process.env.E2E_READER_URL;
    if (!url) throw new Error("E2E_READER_URL が未設定です（task up が e2e/.env.e2e に書き出します）");
    pool = new pg.Pool({ connectionString: url, max: 4 });
  }
  return pool;
}

export function createDb(): Db {
  return {
    async query<T>(sql: string, params: unknown[] = []) {
      const r = await getPool().query(sql, params);
      return r.rows as T[];
    },
    async count(sql: string, params: unknown[] = []) {
      const r = await getPool().query(sql, params);
      return Number(Object.values(r.rows[0] ?? { n: 0 })[0]);
    },
  };
}
