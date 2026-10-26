import { Button, Center, Stack, Text, Title } from "@mantine/core";
import { Link } from "react-router";

export function NotFoundPage() {
  return (
    <Center py={96}>
      <Stack align="center" gap="xs">
        <Title order={2}>ページが見つかりません</Title>
        <Text c="dimmed" size="sm">
          URL が正しいか確認してください。
        </Text>
        <Button component={Link} to="/" variant="light" mt="sm">
          ホームへ戻る
        </Button>
      </Stack>
    </Center>
  );
}
