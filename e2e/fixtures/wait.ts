// wait fixture：固定の sleep を使わず、条件が成り立つまで polling する（platform.md §10.2）。
import type { Db } from "./db.ts";

type Falsy<T> = T | undefined | null | false;

export interface WaitOptions {
  timeout?: number;
  interval?: number;
  message?: string;
}

export interface Wait {
  /** fn が真（または undefined・null・false 以外の値）を返すまで待つ。上限に達したら最後の値を添えて失敗する。 */
  until<T>(fn: () => Falsy<T> | Promise<Falsy<T>>, opts?: WaitOptions): Promise<T>;
  /** その集約の Outbox に event_type のイベントがあり、送信済みになるまで待つ。 */
  untilEvent(eventType: string, opts: { aggregateId?: string; schema?: string } & WaitOptions): Promise<Record<string, unknown>>;
}

export function createWait(db: Db, defaultTimeout: number): Wait {
  const until = async <T>(fn: () => Falsy<T> | Promise<Falsy<T>>, opts: WaitOptions = {}): Promise<T> => {
    const timeout = opts.timeout ?? defaultTimeout;
    const interval = opts.interval ?? 200;
    const deadline = Date.now() + timeout;
    let last: unknown;
    let lastErr: unknown;
    for (;;) {
      try {
        last = await fn();
        lastErr = undefined;
        if (last !== undefined && last !== null && last !== false) return last as T;
      } catch (e) {
        lastErr = e;
      }
      if (Date.now() >= deadline) {
        const detail = lastErr ? `最後のエラー: ${String(lastErr)}` : `最後の値: ${JSON.stringify(last)}`;
        throw new Error(`wait.until: ${timeout}ms 以内に成り立ちませんでした${opts.message ? `（${opts.message}）` : ""}。${detail}`);
      }
      await new Promise((r) => setTimeout(r, interval));
    }
  };
  return {
    until,
    untilEvent: (eventType, opts) => {
      const schema = opts.schema ?? eventType.split(".")[0];
      if (!/^[a-z]+$/.test(schema)) throw new Error(`bad schema ${schema}`);
      return until(async () => {
        const rows = await db.query<Record<string, unknown>>(
          `SELECT * FROM ${schema}.outbox_events WHERE event_type = $1 AND ($2::uuid IS NULL OR aggregate_id = $2::uuid) AND published_at IS NOT NULL ORDER BY occurred_at DESC LIMIT 1`,
          [eventType, opts.aggregateId ?? null],
        );
        return rows[0];
      }, { ...opts, message: opts.message ?? `${eventType} が送信済みになる` });
    },
  };
}
