resource "aws_sqs_queue" "dead_letter" {
  name                      = "${local.name}-work-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_sqs_queue" "work" {
  name                       = "${local.name}-work"
  message_retention_seconds  = 345600
  visibility_timeout_seconds = 300
  receive_wait_time_seconds  = 20
  sqs_managed_sse_enabled    = true

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_sqs_queue_redrive_policy" "work" {
  queue_url = aws_sqs_queue.work.url
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dead_letter.arn
    maxReceiveCount     = 5
  })
}

resource "aws_sqs_queue_redrive_allow_policy" "dead_letter" {
  queue_url = aws_sqs_queue.dead_letter.url
  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.work.arn]
  })
}

# These policies grant nobody access; future runtime IAM roles must separately
# receive queue-specific actions. Deny HTTP even for otherwise allowed callers.
resource "aws_sqs_queue_policy" "tls_only" {
  for_each = {
    work        = aws_sqs_queue.work
    dead_letter = aws_sqs_queue.dead_letter
  }

  queue_url = each.value.url
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "sqs:*"
      Resource  = each.value.arn
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

resource "aws_s3_bucket" "archive" {
  bucket        = "${local.name}-${local.account_id}-archive"
  force_destroy = false

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [terraform_data.deployment_guard]
}

resource "aws_s3_bucket_public_access_block" "archive" {
  bucket                  = aws_s3_bucket.archive.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "archive" {
  bucket = aws_s3_bucket.archive.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_versioning" "archive" {
  bucket = aws_s3_bucket.archive.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "archive" {
  bucket = aws_s3_bucket.archive.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.foundation["archive"].arn
    }
    bucket_key_enabled = true
  }
}

# Explicit headers are required on every write, even though bucket-default KMS
# encryption is set. Future writers must send SSE=aws:kms + this exact key ARN.
resource "aws_s3_bucket_policy" "archive" {
  bucket = aws_s3_bucket.archive.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyInsecureTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:*"
        Resource  = [aws_s3_bucket.archive.arn, "${aws_s3_bucket.archive.arn}/*"]
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid       = "DenyNonKmsWrites"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.archive.arn}/*"
        Condition = { StringNotEquals = { "s3:x-amz-server-side-encryption" = "aws:kms" } }
      },
      {
        Sid       = "DenyUnexpectedEncryptionKey"
        Effect    = "Deny"
        Principal = "*"
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.archive.arn}/*"
        Condition = { StringNotEquals = { "s3:x-amz-server-side-encryption-aws-kms-key-id" = aws_kms_key.foundation["archive"].arn } }
      }
    ]
  })

  depends_on = [aws_s3_bucket_public_access_block.archive]
}

# No archive object/version expiry is assumed without a retention decision.
resource "aws_s3_bucket_lifecycle_configuration" "archive" {
  bucket = aws_s3_bucket.archive.id

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.archive]
}
