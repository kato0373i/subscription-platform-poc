# DB（platform.md §8.4、ADR-008）。ローカルは MiniStack に aws_db_instance（PostgreSQL 16）を作る。
# 本番の Aurora（クラスター・Multi-AZ・KMS・RDS Proxy）は本番インフラの範囲で、このモジュールに足す（PoC では作らない）。
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "enabled" {
  type    = bool
  default = true # DB_MODE=container のときは false（compose の postgres を使う）
}

variable "identifier" {
  type = string
}

variable "engine_version" {
  type    = string
  default = "16.4"
}

variable "instance_class" {
  type    = string
  default = "db.t3.micro"
}

variable "db_name" {
  type    = string
  default = "sp"
}

variable "master_username" {
  type = string
}

variable "master_password" {
  type      = string
  sensitive = true
}

resource "aws_db_instance" "this" {
  count                   = var.enabled ? 1 : 0
  identifier              = var.identifier
  engine                  = "postgres"
  engine_version          = var.engine_version
  instance_class          = var.instance_class
  allocated_storage       = 20
  db_name                 = var.db_name
  username                = var.master_username
  password                = var.master_password
  skip_final_snapshot     = true
  backup_retention_period = 0
  apply_immediately       = true
}

output "address" {
  value = var.enabled ? aws_db_instance.this[0].address : null
}

output "port" {
  value = var.enabled ? aws_db_instance.this[0].port : null
}
