// 見た目は事業者コンソールと同じ（白と薄いグレー、アクセントはインディゴの 1 色）。スマートフォンを主にするので入力は大きめ（consumer-mypage §7）。
import { createTheme, type MantineColorsTuple } from "@mantine/core";

const brand: MantineColorsTuple = ["#f0efff", "#dedcff", "#bcb7ff", "#9790ff", "#7a70ff", "#665cff", "#635bff", "#5048e5", "#4640cc", "#3a33b3"];

export const theme = createTheme({
  primaryColor: "brand",
  primaryShade: 6,
  colors: { brand },
  fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "Hiragino Sans", "Hiragino Kaku Gothic ProN", "Noto Sans JP", Meiryo, sans-serif',
  fontFamilyMonospace: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
  headings: { fontWeight: "700", sizes: { h1: { fontSize: "24px" }, h2: { fontSize: "20px" }, h3: { fontSize: "16px" } } },
  defaultRadius: "md",
  fontSizes: { sm: "13px", md: "14px" },
  black: "#1a1f36",
  components: {
    Button: { defaultProps: { size: "md" } },
    TextInput: { defaultProps: { size: "md" } },
    PasswordInput: { defaultProps: { size: "md" } },
    Paper: { defaultProps: { withBorder: true, radius: "md" } },
    Table: { defaultProps: { verticalSpacing: "sm", horizontalSpacing: "md" } },
    Badge: { defaultProps: { variant: "light", radius: "sm", tt: "none" } },
    Modal: { defaultProps: { centered: true, radius: "md" } },
    // 狭い画面では幅いっぱい（console-dashboard FR-008）
    Drawer: { defaultProps: { position: "right", size: "min(100vw, 440px)" } },
  },
});
