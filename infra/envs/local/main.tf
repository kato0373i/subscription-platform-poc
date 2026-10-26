# MiniStack 向けのローカル環境（platform.md §8.4）。provider の endpoints を MiniStack に向ける。state はローカルファイル（gitignore）。
terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "aws_endpoint" {
  type    = string
  default = "http://localhost:4566" # terraform をコンテナで動かすときは http://ministack:4566
}

variable "rds_enabled" {
  type    = bool
  default = true # DB_MODE=container のときは false
}

variable "scheduler_enabled" {
  type    = bool
  default = true # SCHEDULER_MODE=internal のときは false
}

variable "db_master_password" {
  type      = string
  default   = "spmasterpw"
  sensitive = true
}

provider "aws" {
  region                      = "ap-northeast-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  s3_use_path_style           = true
  endpoints {
    rds         = var.aws_endpoint
    sqs         = var.aws_endpoint
    eventbridge = var.aws_endpoint
    scheduler   = var.aws_endpoint
    logs        = var.aws_endpoint
    ses         = var.aws_endpoint
    cognitoidp  = var.aws_endpoint
    iam         = var.aws_endpoint
    sts         = var.aws_endpoint
  }
}

module "data" {
  source          = "../../modules/data"
  enabled         = var.rds_enabled
  identifier      = "sp-local"
  master_username = "spmaster"
  master_password = var.db_master_password
}

module "messaging" {
  source            = "../../modules/messaging"
  messaging_file    = "${path.module}/../../../deploy/messaging.yaml"
  schedules_file    = "${path.module}/../../../deploy/schedules.yaml"
  scheduler_enabled = var.scheduler_enabled
}

# ログ（Fluent Bit の出力先。本番の /sp/<env>/<service> と同じ名前の付け方。§8.6.4）
resource "aws_cloudwatch_log_group" "service" {
  for_each          = toset(["api", "worker-billing", "worker-integration", "worker-projection", "migrate", "unknown"])
  name              = "/sp/local/${each.key}"
  retention_in_days = 7
}

# メールの送信元（integration §3.4）
resource "aws_ses_email_identity" "noreply" {
  email = "noreply@sp.local"
}

# 運営者のプール（運営者コンソール。本番と同じ定義のモジュール。operator-console §5.3）
module "operator_auth" {
  source      = "../../modules/operator_auth"
  name        = "sp-local-operator"
  console_url = "http://localhost:8090/ops/login"
}

# 契約者のプール（マイページ。本番と同じ定義のモジュール。consumer-mypage §5.4）
module "consumer_auth" {
  source = "../../modules/consumer_auth"
  name   = "sp-local-consumer"
}

# スタッフのプール（事業者コンソール。本番と同じ定義のモジュール。business-console §5.3）
module "staff_auth" {
  source      = "../../modules/staff_auth"
  name        = "sp-local-staff"
  console_url = "http://localhost:8090/console/login"
}


output "db_address" {
  value = module.data.address
}

output "db_port" {
  value = module.data.port
}

output "queue_urls" {
  value = module.messaging.queue_urls
}

output "cognito" {
  value = merge(
    { operator = { id = module.operator_auth.user_pool_id, client_id = module.operator_auth.client_id } },
    { staff = { id = module.staff_auth.user_pool_id, client_id = module.staff_auth.client_id } },
    { consumer = { id = module.consumer_auth.user_pool_id, client_id = module.consumer_auth.client_id } },
  )
}
