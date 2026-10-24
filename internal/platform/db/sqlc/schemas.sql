-- sqlc だけが読む。実際のスキーマは cmd/migrate bootstrap が作る（platform.md §6.1）。
CREATE SCHEMA platform;
CREATE SCHEMA tenant;
CREATE SCHEMA customer;
CREATE SCHEMA catalog;
CREATE SCHEMA subscription;
CREATE SCHEMA billing;
CREATE SCHEMA payments;
CREATE SCHEMA orchestration;
CREATE SCHEMA integration;
CREATE SCHEMA audit;
