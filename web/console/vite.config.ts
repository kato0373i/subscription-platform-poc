// 事業者コンソール（business-console の設計 §5.5・§5.7）。/console/ 配下に配り、開発中は /v1 を api に中継する（本番と同じ同一オリジン）。
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  base: "/console/",
  plugins: [react()],
  build: {
    target: ["chrome120", "edge120", "firefox121", "safari17"], // NFR-002：主要ブラウザの最新 2 版
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: { mantine: ["@mantine/core", "@mantine/hooks", "@mantine/notifications", "@mantine/modals", "@mantine/dates"], amplify: ["aws-amplify"] },
      },
    },
  },
  server: {
    port: 5173,
    proxy: { "/v1": "http://localhost:8080" },
  },
});
