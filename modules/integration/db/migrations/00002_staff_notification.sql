-- スタッフ宛ての通知（business-console の設計 §4。既存の Cognito アカウントの人を事業者に追加したとき）
-- +goose Up
ALTER TABLE integration.email_notifications
  DROP CONSTRAINT email_notifications_recipient_type_check,
  ADD CONSTRAINT email_notifications_recipient_type_check CHECK (recipient_type IN ('customer','tenant_owner','staff')),
  ADD COLUMN staff_id uuid NULL,
  ADD CONSTRAINT email_notifications_staff CHECK (recipient_type <> 'staff' OR staff_id IS NOT NULL);

-- +goose Down
ALTER TABLE integration.email_notifications
  DROP CONSTRAINT email_notifications_staff,
  DROP COLUMN staff_id,
  DROP CONSTRAINT email_notifications_recipient_type_check,
  ADD CONSTRAINT email_notifications_recipient_type_check CHECK (recipient_type IN ('customer','tenant_owner'));
