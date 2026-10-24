-- platform スキーマの Outbox・Inbox（配線の確認用イベント platform.smoke_requested に使う）
-- +goose Up
SELECT platform.create_messaging_tables('platform');

-- +goose Down
DROP TABLE platform.processed_messages;
DROP TABLE platform.outbox_events;
