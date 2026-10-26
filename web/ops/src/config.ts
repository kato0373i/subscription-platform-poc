// ランタイムの設定（/ops/config.json。ビルドは 1 つで、環境ごとに差し替える。operator-console §7）。
export interface ConsoleConfig {
  cognito: { region: string; userPoolId: string; clientId: string; endpoint?: string };
}

export async function loadConfig(): Promise<ConsoleConfig> {
  const res = await fetch(`${import.meta.env.BASE_URL}config.json`, { cache: "no-store" });
  if (!res.ok) throw new Error(`config.json: ${res.status}`);
  return (await res.json()) as ConsoleConfig;
}
