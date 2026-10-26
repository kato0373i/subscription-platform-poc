# 運営者用の Cognito ユーザープール（operator-console の設計 §5.3）。本番と local で同じ定義を使う。
# アカウントはインフラの管理者が作り、グループ operators に入れる（自己登録なし）。ops API はこのグループの ID トークンだけを受け付ける。
variable "name" { type = string }
variable "console_url" {
  type        = string
  description = "仮パスワードのメールに載せる運営者コンソールの URL"
}
variable "ses_source_arn" {
  type    = string
  default = ""
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

  password_policy {
    minimum_length                   = 12
    require_lowercase                = true
    require_uppercase                = true
    require_numbers                  = true
    require_symbols                  = true
    temporary_password_validity_days = 3
  }

  admin_create_user_config {
    allow_admin_create_user_only = true
    invite_message_template {
      email_subject = "【運営者コンソール】アカウントの発行"
      email_message = <<-EOT
        運営者コンソールのアカウントを発行しました。
        次の URL から、メールアドレスと仮パスワードでログインし、新しいパスワードを設定してください。

        ログイン URL：${var.console_url}
        メールアドレス：{username}
        仮パスワード：{####}
      EOT
      sms_message   = "仮パスワード：{####}（ユーザー名 {username}）"
    }
  }

  account_recovery_setting {
    recovery_mechanism {
      name     = "verified_email"
      priority = 1
    }
  }

  verification_message_template {
    default_email_option = "CONFIRM_WITH_CODE"
    email_subject        = "【運営者コンソール】確認コード"
    email_message        = "パスワード再設定の確認コードは {####} です。"
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

resource "aws_cognito_user_group" "operators" {
  name         = "operators"
  user_pool_id = aws_cognito_user_pool.this.id
  description  = "運営者（ops API を使える）"
}

resource "aws_cognito_user_pool_client" "ops" {
  name                          = "${var.name}-ops"
  user_pool_id                  = aws_cognito_user_pool.this.id
  generate_secret               = false
  explicit_auth_flows           = ["ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]
  prevent_user_existence_errors = "ENABLED"
  enable_token_revocation       = true
  access_token_validity         = 1
  id_token_validity             = 1
  refresh_token_validity        = 8 # ログインの上限（要件 FR-001）
  token_validity_units {
    access_token  = "hours"
    id_token      = "hours"
    refresh_token = "hours"
  }
}

output "user_pool_id" { value = aws_cognito_user_pool.this.id }
output "client_id" { value = aws_cognito_user_pool_client.ops.id }
output "user_pool_arn" { value = aws_cognito_user_pool.this.arn }
