import { Box, Center, Paper, Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

export function AuthCard({ title, description, children }: { title: string; description?: ReactNode; children: ReactNode }) {
  return (
    <Center mih="100vh" bg="#f6f8fa" p="md">
      <Stack w={400} gap="lg">
        <Text fw={700} size="lg" c="brand.6" ta="center">
          運営者コンソール
        </Text>
        <Paper p="xl" radius="md" shadow="xs">
          <Title order={2} mb={description ? 4 : "lg"}>
            {title}
          </Title>
          {description && (
            <Text size="sm" c="dimmed" mb="lg">
              {description}
            </Text>
          )}
          <Box>{children}</Box>
        </Paper>
      </Stack>
    </Center>
  );
}

export const PASSWORD_HINT = "12 文字以上で、英大文字・英小文字・数字・記号をそれぞれ含めてください。";

export function passwordProblem(pw: string): string | null {
  if (pw.length < 12 || !/[a-z]/.test(pw) || !/[A-Z]/.test(pw) || !/[0-9]/.test(pw) || !/[^A-Za-z0-9]/.test(pw)) return PASSWORD_HINT;
  return null;
}
