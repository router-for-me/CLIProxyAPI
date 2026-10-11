provider "aws" {
  region = var.region
}

data "aws_caller_identity" "current" {}

# Ubuntu 24.04 LTS for Graviton, resolved through Canonical's public SSM parameter so the example never goes stale.
data "aws_ssm_parameter" "ubuntu_arm64" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/arm64/hvm/ebs-gp3/ami-id"
}

locals {
  tags = merge({ Project = var.name }, var.tags)

  user_data = templatefile("${path.module}/user-data.sh.tftpl", {
    name              = var.name
    release_tag       = var.release_tag
    binary_sha256     = var.binary_sha256
    private_ip        = var.private_ip
    port              = var.port
    server_ports      = var.server_ports
    drain_seconds     = var.drain_seconds
    envoy_version     = var.envoy_version
    envoy_sha256      = var.envoy_sha256
    region            = var.region
    profiles          = var.profiles
    enable_cloudwatch = var.enable_cloudwatch
    metrics_namespace = var.metrics_namespace
    log_group         = "/${var.name}/server"
    cwagent_version   = var.cwagent_version
    cwagent_sha256    = var.cwagent_sha256_arm64
    backup_secret_id  = var.backup_api_key_secret_id
  })
}

# Only the proxy port, only from the client networks. There is no SSH rule at all: administer the box through
# Session Manager (the instance role below carries AmazonSSMManagedInstanceCore).
resource "aws_security_group" "server" {
  name_prefix = "${var.name}-"
  description = "claude-master: the proxy port from the client networks, nothing else inbound"
  vpc_id      = var.vpc_id

  ingress {
    description = "claude-master proxy (TLS, client certificate required)"
    from_port   = var.port
    to_port     = var.port
    protocol    = "tcp"
    cidr_blocks = var.client_cidrs
  }

  egress {
    description      = "all outbound (api.anthropic.com, the release download, AWS APIs)"
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  tags = merge(local.tags, { Name = var.name })

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_iam_role" "server" {
  name = "${var.name}-server"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "ssm" {
  role       = aws_iam_role.server.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# The CloudWatch agent may publish metrics in ONE namespace and write to ONE log group, and read nothing.
resource "aws_cloudwatch_log_group" "server" {
  count             = var.enable_cloudwatch ? 1 : 0
  name              = "/${var.name}/server"
  retention_in_days = var.log_retention_days
  tags              = local.tags
}

resource "aws_iam_role_policy" "telemetry" {
  count = var.enable_cloudwatch ? 1 : 0
  name  = "telemetry"
  role  = aws_iam_role.server.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "PublishMetricsInOneNamespace"
        Effect    = "Allow"
        Action    = "cloudwatch:PutMetricData"
        Resource  = "*"
        Condition = { StringEquals = { "cloudwatch:namespace" = var.metrics_namespace } }
      },
      {
        Sid      = "WriteItsOwnLogGroup"
        Effect   = "Allow"
        Action   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
        Resource = [aws_cloudwatch_log_group.server[0].arn, "${aws_cloudwatch_log_group.server[0].arn}:*"]
      },
    ]
  })
}

# Optional: the one secret holding the paid API-key backup, and nothing else in Secrets Manager.
resource "aws_iam_role_policy" "backup_key" {
  count = var.backup_api_key_secret_id == "" ? 0 : 1
  name  = "backup-api-key"
  role  = aws_iam_role.server.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "ReadTheBackupKeyOnly"
      Effect   = "Allow"
      Action   = "secretsmanager:GetSecretValue"
      Resource = startswith(var.backup_api_key_secret_id, "arn:") ? var.backup_api_key_secret_id : "arn:aws:secretsmanager:${var.region}:${data.aws_caller_identity.current.account_id}:secret:${var.backup_api_key_secret_id}-*"
    }]
  })
}

resource "aws_iam_instance_profile" "server" {
  name = "${var.name}-server"
  role = aws_iam_role.server.name
}

resource "aws_instance" "server" {
  ami                         = nonsensitive(data.aws_ssm_parameter.ubuntu_arm64.value)
  instance_type               = var.instance_type
  subnet_id                   = var.subnet_id
  private_ip                  = var.private_ip
  associate_public_ip_address = var.assign_public_ip
  vpc_security_group_ids      = [aws_security_group.server.id]
  iam_instance_profile        = aws_iam_instance_profile.server.name
  user_data                   = local.user_data

  # The root volume holds the subscription logins and the CA key that signs every client certificate: the two things
  # that are painful to recreate. It is encrypted and kept when the instance is destroyed.
  root_block_device {
    volume_size           = 20
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = false
  }

  metadata_options {
    http_tokens = "required" # IMDSv2 only
  }

  # Changing the bootstrap or the AMI must not replace the box that holds the logins. Roll the binary forward by
  # editing release_tag and binary_sha256, then re-running the bootstrap (see the README: "Rolling forward").
  lifecycle {
    ignore_changes = [ami, user_data]
  }

  tags = merge(local.tags, { Name = var.name })
}
