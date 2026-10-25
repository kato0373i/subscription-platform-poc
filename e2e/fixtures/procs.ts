// procs fixture：プロセスを止める・起動し直す（orchestration.md §11b.1。E2E-014 だけが使う）。
// プロセスは compose の profile app のコンテナで動いている前提（task dev:container・CI）。go run（task dev）では使えない。
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

export type Proc = "api" | "worker-billing" | "worker-integration" | "worker-projection";

export interface Procs {
  kill(name: Proc): Promise<void>;
  up(name: Proc): Promise<void>;
  /** テストが途中で失敗しても、止めたプロセスを起動し直す（後続のテストを壊さない）。 */
  restoreAll(): Promise<void>;
}

const root = fileURLToPath(new URL("../..", import.meta.url));

function compose(...args: string[]): string {
  return execFileSync("docker", ["compose", "--profile", "app", ...args], { cwd: root, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], timeout: 180_000 });
}

// 落ちたプロセスが出していた SQS の受信（ロングポーリング。platform の既定 20 秒）は、相手が消えても MiniStack 側では
// 待ち時間が切れるまで残り、その間に届いたメッセージを受け取ってしまう（可視性タイムアウトの後まで戻らない）。
// up は、停止からこの時間がたつまで待ってから戻る。
const orphanPollMs = 21_000;

export function createProcs(): Procs {
  const touched = new Set<Proc>();
  const killedAt = new Map<Proc, number>();
  const ensureContainer = (name: Proc) => {
    if (compose("ps", "-q", name).trim() === "") {
      throw new Error(`procs: ${name} のコンテナが動いていません。プロセスを止めるテストは task dev:container で起動した環境で流してください`);
    }
  };
  return {
    async kill(name) {
      ensureContainer(name);
      touched.add(name);
      compose("kill", "-s", "SIGKILL", name); // 後始末をさせずに落とす（タスクの異常終了）
      killedAt.set(name, Date.now());
    },
    async up(name) {
      compose("up", "-d", "--wait", name); // ヘルスチェック（/readyz）が通るまで待つ
      const left = (killedAt.get(name) ?? 0) + orphanPollMs - Date.now();
      if (left > 0) await new Promise((r) => setTimeout(r, left));
      touched.delete(name);
    },
    async restoreAll() {
      for (const name of touched) compose("up", "-d", "--wait", name);
      touched.clear();
    },
  };
}
