// 起動：設定（config.json）を読み、Amplify を設定してから描画する。
import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import { MantineProvider } from "@mantine/core";
import { ModalsProvider } from "@mantine/modals";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiError } from "./api/client.ts";
import { App } from "./App.tsx";
import { AuthProvider, configureAmplify } from "./auth/AuthProvider.tsx";
import { loadConfig } from "./config.ts";
import { theme } from "./theme.ts";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: true,
      retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2,
    },
  },
});

async function main() {
  const cfg = await loadConfig();
  configureAmplify(cfg);
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <MantineProvider theme={theme} defaultColorScheme="light">
        <ModalsProvider labels={{ confirm: "OK", cancel: "やめる" }}>
          <Notifications position="top-center" />
          <QueryClientProvider client={queryClient}>
            <AuthProvider>
              <App />
            </AuthProvider>
          </QueryClientProvider>
        </ModalsProvider>
      </MantineProvider>
    </StrictMode>,
  );
}

void main();
