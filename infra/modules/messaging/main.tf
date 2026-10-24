# バス・ルール・SQS（＋DLQ）・キューポリシー・Scheduler を deploy/messaging.yaml・schedules.yaml から作る（platform.md §8.1・§8.4）。
# 環境に依存しないので、将来 dev/stg/prod でもそのまま使う。
terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.0" }
  }
}

variable "messaging_file" {
  type = string
}

variable "schedules_file" {
  type = string
}

variable "scheduler_enabled" {
  type    = bool
  default = true # SCHEDULER_MODE=internal のときは false
}

locals {
  topology  = yamldecode(file(var.messaging_file))
  schedules = yamldecode(file(var.schedules_file))
  queues = { for q in local.topology.queues : q.name => merge({
    fifo                 = false
    kind                 = "events"
    events               = []
    visibility_timeout_s = 60
    max_receive          = 5
  }, q) }
  event_queues = { for k, q in local.queues : k => q if q.kind == "events" && !q.fifo && length(q.events) > 0 }
  scheduled    = { for j in local.schedules.jobs : j.name => j if try(j.schedule, "") != "" }
}

resource "aws_cloudwatch_event_bus" "this" {
  name = local.topology.bus
}

resource "aws_sqs_queue" "dlq" {
  for_each                    = local.queues
  name                        = each.value.fifo ? "${trimsuffix(each.key, ".fifo")}-dlq.fifo" : "${each.key}-dlq"
  fifo_queue                  = each.value.fifo
  message_retention_seconds   = 1209600
}

resource "aws_sqs_queue" "this" {
  for_each                    = local.queues
  name                        = each.key
  fifo_queue                  = each.value.fifo
  content_based_deduplication = false
  visibility_timeout_seconds  = each.value.visibility_timeout_s
  receive_wait_time_seconds   = 20
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = each.value.max_receive
  })
}

# events は detail-type の完全一致と prefix: だけ（Go の振り分けと同じ意味）
resource "aws_cloudwatch_event_rule" "this" {
  for_each       = local.event_queues
  name           = "to-${each.key}"
  event_bus_name = aws_cloudwatch_event_bus.this.name
  # 完全一致（文字列）と prefix（オブジェクト）が混ざるので、要素ごとに JSON にしてからつなぐ
  event_pattern = "{\"detail-type\":[${join(",", [for e in each.value.events : startswith(e, "prefix:") ? jsonencode({ prefix = trimprefix(e, "prefix:") }) : jsonencode(e)])}]}"
}

resource "aws_cloudwatch_event_target" "this" {
  for_each       = local.event_queues
  rule           = aws_cloudwatch_event_rule.this[each.key].name
  event_bus_name = aws_cloudwatch_event_bus.this.name
  arn            = aws_sqs_queue.this[each.key].arn
}

resource "aws_sqs_queue_policy" "this" {
  for_each  = local.event_queues
  queue_url = aws_sqs_queue.this[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.this[each.key].arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_cloudwatch_event_rule.this[each.key].arn } }
    }]
  })
}

resource "aws_iam_role" "scheduler" {
  count = var.scheduler_enabled ? 1 : 0
  name  = "sp-scheduler-to-sqs"
  assume_role_policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Principal = { Service = "scheduler.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy" "scheduler" {
  count = var.scheduler_enabled ? 1 : 0
  role  = aws_iam_role.scheduler[0].id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "sqs:SendMessage", Resource = [for k, q in aws_sqs_queue.this : q.arn if startswith(k, "scheduler-")] }]
  })
}

# EventBridge Scheduler（Asia/Tokyo）→ プロセスごとの scheduler キュー。本文 {job, trigger:"schedule"}
resource "aws_scheduler_schedule" "this" {
  for_each                     = var.scheduler_enabled ? local.scheduled : {}
  name                         = each.key
  schedule_expression          = each.value.schedule
  schedule_expression_timezone = "Asia/Tokyo"
  flexible_time_window { mode = "OFF" }
  target {
    arn      = aws_sqs_queue.this["scheduler-${trimprefix(each.value.process, "worker-")}"].arn
    role_arn = aws_iam_role.scheduler[0].arn
    input    = jsonencode({ job = each.key, trigger = "schedule" })
  }
}

output "queue_urls" {
  value = { for k, q in aws_sqs_queue.this : k => q.id }
}

output "bus_name" {
  value = aws_cloudwatch_event_bus.this.name
}
