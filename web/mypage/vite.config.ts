// 契約者のマイページ（consumer-mypage の設計 §7）。/mypage/ 配下に配り、開発中は /v1 と /checkout を api に中継する（本番と同じ同一オリジン）。
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  base: "/mypage/",
  plugins: [react()],
  build: {
    target: ["chrome120", "edge120", "firefox121", "safari17"],
    sourcemap: true,
    rollupOptions: {
      output: {
        manualChunks: { mantine: ["@mantine/core", "@mantine/hooks", "@mantine/notifications", "@mantine/modals"], amplify: ["aws-amplify"] },
      },
    },
  },
  server: {
    port: 5174,
    proxy: { "/v1": "http://localhost:8080", "/checkout": "http://localhost:8080" },
  },
});
