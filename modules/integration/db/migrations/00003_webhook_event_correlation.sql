-- 外部イベントに元のイベントの相関 ID を持たせる（配信の http.outbound.completed を申込から追えるようにする。platform.md §8.6・AC-PLT-073）
-- +goose Up
ALTER TABLE integration.webhook_events ADD COLUMN correlation_id text NULL CHECK (char_length(correlation_id) <= 128);

-- +goose Down
ALTER TABLE integration.webhook_events DROP COLUMN correlation_id;
