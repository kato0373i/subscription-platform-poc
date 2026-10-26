# 契約者用の Cognito ユーザープール（consumer-mypage の設計 §5.4）。本番と local で同じ定義を使う。
variable "name" { type = string }
variable "ses_source_arn" {
  type        = string
  default     = ""
  description = "本番：確認コード・再設定メールの送信元（SES の ID の ARN）。空なら Cognito の既定の送信（local）"
}
variable "ses_from" {
  type    = string
  default = ""
}

resource "aws_cognito_user_pool" "this" {
  name                     = var.name
  username_attributes      = ["email"]
  auto_verified_attributes = ["email"]

  username_configuration {
    case_sensitive = false
  }

  # 契約者の使いやすさを優先してスタッフより緩くする（要件 FR-001）
  password_policy {
    minimum_length    = 8
    require_lowercase = true
    require_uppercase = false
    require_numbers   = true
    require_symbols   = false
  }

  schema {
    name                = "name"
    attribute_data_type = "String"
    required            = true
    mutable             = true
    string_attribute_constraints {
      min_length = 1
      max_length = 100
    }
  }

  admin_create_user_config {
    allow_admin_create_user_only = false # 自己登録を許可する
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }

  verification_message_template {
    default_email_option = "CONFIRM_WITH_CODE"
    email_subject        = "【マイページ】確認コード"
    email_message        = "確認コードは {####} です。画面に入力して手続きを完了してください。"
  }

  dynamic "email_configuration" {
    for_each = var.ses_source_arn == "" ? [] : [1]
    content {
      email_sending_account = "DEVELOPER"
      source_arn            = var.ses_source_arn
      from_email_address    = var.ses_from
    }
  }
}

resource "aws_cognito_user_pool_client" "mypage" {
  name                          = "${var.name}-mypage"
  user_pool_id                  = aws_cognito_user_pool.this.id
  generate_secret               = false
  explicit_auth_flows           = ["ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]
  prevent_user_existence_errors = "ENABLED" # ログイン・再設定でアカウントの有無を明かさない（NFR-005）
  enable_token_revocation       = true
  read_attributes               = ["email", "email_verified", "name"]
  write_attributes              = ["email", "name"]
  access_token_validity         = 1
  id_token_validity             = 1
  refresh_token_validity        = 30 # ログインの状態を 30 日保つ（FR-003）
  token_validity_units {
    access_token  = "hours"
    id_token      = "hours"
    refresh_token = "days"
  }
}

output "user_pool_id" { value = aws_cognito_user_pool.this.id }
output "client_id" { value = aws_cognito_user_pool_client.mypage.id }
output "user_pool_arn" { value = aws_cognito_user_pool.this.arn }
