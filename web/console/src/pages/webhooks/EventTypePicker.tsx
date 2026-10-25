// 購読するイベントの選択（すべて選ぶ・グループごとのチェック）。
import { Checkbox, Group, Paper, SimpleGrid, Text } from "@mantine/core";
import { eventTypes } from "./events.ts";

export function EventTypePicker({ value, onChange, error }: { value: string[]; onChange: (v: string[]) => void; error?: React.ReactNode }) {
  const all = value.length === eventTypes.length;
  return (
    <div>
      <Group justify="space-between" mb={4}>
        <Text size="sm" fw={500}>
          送るイベント <Text span c="red">*</Text>
        </Text>
        <Checkbox
          size="xs"
          label="すべて"
          checked={all}
          indeterminate={value.length > 0 && !all}
          onChange={() => onChange(all ? [] : eventTypes.map((e) => e.value))}
        />
      </Group>
      <Paper p="sm" withBorder style={error ? { borderColor: "var(--mantine-color-red-6)" } : undefined}>
        <Checkbox.Group value={value} onChange={onChange}>
          <SimpleGrid cols={1} spacing={6}>
            {eventTypes.map((e) => (
              <Checkbox
                key={e.value}
                value={e.value}
                label={
                  <span>
                    {e.label}{" "}
                    <Text span size="xs" c="dimmed" ff="monospace">
                      {e.value}
                    </Text>
                  </span>
                }
              />
            ))}
          </SimpleGrid>
        </Checkbox.Group>
      </Paper>
      {error && (
        <Text size="xs" c="red" mt={4}>
          {error}
        </Text>
      )}
    </div>
  );
}
