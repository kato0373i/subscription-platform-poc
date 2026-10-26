// 認証の画面の枠。
import { Box, Paper, Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

export function AuthCard({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <Box bg="#f6f8fa" mih="100vh" px="md" py={48}>
      <Stack maw={420} mx="auto" gap="lg">
        <Text fw={700} size="lg" c="brand.6" ta="center">
          マイページ
        </Text>
        <Paper p="xl">
          <Title order={2} fz={20} mb={description ? 4 : "md"}>
            {title}
          </Title>
          {description && (
            <Text size="sm" c="dimmed" mb="md">
              {description}
            </Text>
          )}
          {children}
        </Paper>
      </Stack>
    </Box>
  );
}
