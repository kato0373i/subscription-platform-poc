-- スタッフの招待（business-console の設計 §3・§4）
-- +goose Up
ALTER TABLE tenant.staff_members
  ADD COLUMN status       text NOT NULL DEFAULT 'active' CHECK (status IN ('invited','active')),
  ADD COLUMN invited_at   timestamptz NULL,
  ADD COLUMN last_seen_at timestamptz NULL,
  ADD CONSTRAINT staff_members_invited CHECK (status = 'active' OR invited_at IS NOT NULL);
-- ログイン直後の所属する事業者の一覧（全事業者を横断。system スコープ）
CREATE INDEX staff_members_subject_idx ON tenant.staff_members (subject);

-- +goose Down
DROP INDEX tenant.staff_members_subject_idx;
ALTER TABLE tenant.staff_members DROP CONSTRAINT staff_members_invited, DROP COLUMN last_seen_at, DROP COLUMN invited_at, DROP COLUMN status;
