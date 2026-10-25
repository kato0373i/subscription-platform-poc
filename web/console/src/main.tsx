// 起動：設定（config.json）を読み、Amplify を設定してから描画する。
import "@mantine/core/styles.css";
import "@mantine/dates/styles.css";
import "@mantine/notifications/styles.css";
import "dayjs/locale/ja";
import { MantineProvider } from "@mantine/core";
import { DatesProvider } from "@mantine/dates";
import { ModalsProvider } from "@mantine/modals";
import { Notifications } from "@mantine/notifications";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fetchAuthSession } from "aws-amplify/auth";
import { StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { ApiError, reportClientError, type TokenSource } from "./api/client.ts";
import { App } from "./App.tsx";
import { AuthProvider, configureAmplify } from "./auth/AuthProvider.tsx";
import { loadConfig } from "./config.ts";
import { theme } from "./theme.ts";

const token: TokenSource = async (force) => (await fetchAuthSession({ forceRefresh: !!force })).tokens?.idToken?.toString() ?? "";
const tenantOfUrl = () => /\/t\/([^/]+)/.exec(window.location.pathname)?.[1];

/** 想定外のエラー（5xx・通信以外）をサーバーに送る（FR-016・NFR-007）。 */
function report(e: unknown) {
  if (e instanceof ApiError) {
    if (e.status < 500) return;
    reportClientError(token, { message: `${e.status} ${e.code}`, tenantId: tenantOfUrl(), correlationId: e.correlationId ?? undefined });
    return;
  }
  const err = e instanceof Error ? e : new Error(String(e));
  reportClientError(token, { message: err.message, stack: err.stack, tenantId: tenantOfUrl() });
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2,
    },
  },
  queryCache: new QueryCache({
    onError: (e) => {
      // 権限が変わった（ロールの変更・削除）かもしれないので、所属を読み直す（FR-005）
      if (e instanceof ApiError && e.status === 403) void queryClient.invalidateQueries({ queryKey: ["memberships"] });
      report(e);
    },
  }),
  mutationCache: new MutationCache({
    onError: (e) => {
      if (e instanceof ApiError && e.status === 403) void queryClient.invalidateQueries({ queryKey: ["memberships"] });
      report(e);
    },
  }),
});

function GlobalErrorHandlers() {
  useEffect(() => {
    const onError = (ev: ErrorEvent) => report(ev.error ?? ev.message);
    const onRejection = (ev: PromiseRejectionEvent) => report(ev.reason);
    window.addEventListener("error", onError);
    window.addEventListener("unhandledrejection", onRejection);
    return () => {
      window.removeEventListener("error", onError);
      window.removeEventListener("unhandledrejection", onRejection);
    };
  }, []);
  return null;
}

async function main() {
  const cfg = await loadConfig();
  configureAmplify(cfg);
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <MantineProvider theme={theme} defaultColorScheme="light">
        <DatesProvider settings={{ locale: "ja", timezone: "Asia/Tokyo" }}>
          <ModalsProvider labels={{ confirm: "OK", cancel: "キャンセル" }}>
            <Notifications position="top-right" />
            <QueryClientProvider client={queryClient}>
              <AuthProvider>
                <GlobalErrorHandlers />
                <App />
              </AuthProvider>
            </QueryClientProvider>
          </ModalsProvider>
        </DatesProvider>
      </MantineProvider>
    </StrictMode>,
  );
}

void main();
