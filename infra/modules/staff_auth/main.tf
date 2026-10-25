# スタッフ用の Cognito ユーザープール（business-console の設計 §5.3）。本番と local で同じ定義を使う。
variable "name" { type = string }
variable "console_url" {
  type        = string
  description = "招待メールに載せる事業者コンソールの URL"
}
variable "ses_source_arn" {
  type        = string
  default     = ""
  description = "本番：招待・再設定メールの送信元（SES の ID の ARN）。空なら Cognito の既定の送信（local）"
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
    temporary_password_validity_days = 7 # 招待の有効期限（FR-006）
  }

  admin_create_user_config {
    allow_admin_create_user_only = true # セルフサインアップなし（招待だけ）
    invite_message_template {
      email_subject = "【事業者コンソール】招待のお知らせ"
      email_message = <<-EOT
        事業者コンソールに招待されました。
        次の URL から、メールアドレスと仮パスワードでログインし、新しいパスワードを設定してください。

        ログイン URL：${var.console_url}
        メールアドレス：{username}
        仮パスワード：{####}

        仮パスワードの有効期限は 7 日です。期限が切れた場合は、招待した方に再送を依頼してください。
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
    email_subject        = "【事業者コンソール】確認コード"
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

resource "aws_cognito_user_pool_client" "console" {
  name                          = "${var.name}-console"
  user_pool_id                  = aws_cognito_user_pool.this.id
  generate_secret               = false
  explicit_auth_flows           = ["ALLOW_USER_SRP_AUTH", "ALLOW_REFRESH_TOKEN_AUTH"]
  prevent_user_existence_errors = "ENABLED" # アカウントの有無を明かさない（FR-001・FR-002）
  enable_token_revocation       = true      # ログアウトでリフレッシュトークンを失効させる（FR-003）
  access_token_validity         = 1
  id_token_validity             = 1
  refresh_token_validity        = 24 # ログインからの絶対上限（FR-003）
  token_validity_units {
    access_token  = "hours"
    id_token      = "hours"
    refresh_token = "hours"
  }
}

output "user_pool_id" { value = aws_cognito_user_pool.this.id }
output "client_id" { value = aws_cognito_user_pool_client.console.id }
output "user_pool_arn" { value = aws_cognito_user_pool.this.arn }
