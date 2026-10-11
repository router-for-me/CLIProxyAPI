output "instance_id" {
  description = "Instance id. Reach it with: aws ssm start-session --target <id>"
  value       = aws_instance.server.id
}

output "proxy_address" {
  description = "host:port that clients connect to. Client certificates name this address."
  value       = "${var.private_ip}:${var.port}"
}

output "profiles" {
  description = "The subscription profiles to log in, in fallback order."
  value       = var.profiles
}

output "login_command" {
  description = "Run once per profile from a machine with Session Manager access (it is interactive: you paste a code)."
  value       = [for p in var.profiles : "aws ssm start-session --target ${aws_instance.server.id} --document-name AWS-StartInteractiveCommand --parameters 'command=[\"sudo claude-master-login ${p}\"]'"]
}

output "metrics_namespace" {
  description = "CloudWatch namespace of the proxy's metrics (empty when CloudWatch is off)."
  value       = var.enable_cloudwatch ? var.metrics_namespace : ""
}

output "log_group" {
  description = "CloudWatch log group of the proxy log (empty when CloudWatch is off)."
  value       = var.enable_cloudwatch ? aws_cloudwatch_log_group.server[0].name : ""
}

output "bootstrap_script" {
  description = "The rendered bootstrap. The instance ignores user_data changes, so run this on the box (see the guide: Rolling the binary forward) after changing release_tag or binary_sha256."
  value       = local.user_data
}
