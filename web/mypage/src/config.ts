// ランタイムの設定（/mypage/config.json。ビルドは 1 つで、環境ごとに差し替える。consumer-mypage §7）。
export interface MypageConfig {
  cognito: { region: string; userPoolId: string; clientId: string; endpoint?: string };
}

export async function loadConfig(): Promise<MypageConfig> {
  const res = await fetch(`${import.meta.env.BASE_URL}config.json`, { cache: "no-store" });
  if (!res.ok) throw new Error(`config.json: ${res.status}`);
  return (await res.json()) as MypageConfig;
}
