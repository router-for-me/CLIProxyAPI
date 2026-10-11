variable "region" {
  description = "AWS region to deploy into."
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Name prefix for every resource."
  type        = string
  default     = "claude-master"
}

variable "vpc_id" {
  description = "VPC the server lives in."
  type        = string
}

variable "subnet_id" {
  description = "Subnet for the server. It needs outbound internet (api.anthropic.com and the release download): a public subnet with assign_public_ip, or a private one behind a NAT gateway."
  type        = string
}

variable "private_ip" {
  description = "A FIXED private address inside subnet_id. Client certificates name this address, so it must not change."
  type        = string
}

variable "client_cidrs" {
  description = "CIDR blocks allowed to reach the proxy port (the subnets your client boxes are in). Never 0.0.0.0/0: the proxy refuses a public bind, and the security group should say the same."
  type        = list(string)
}

variable "assign_public_ip" {
  description = "Give the server a public IPv4 for OUTBOUND traffic only. The security group admits no inbound connection from outside either way. Set false when the subnet routes through a NAT gateway."
  type        = bool
  default     = true
}

variable "port" {
  description = "Port clients dial (Envoy listens here and passes TCP through to the active server)."
  type        = number
  default     = 8443
}

variable "server_ports" {
  description = "The two servers behind Envoy, blue and green, each on its own port of private_ip (not opened in the security group). A rollout moves new connections from one to the other."
  type        = object({ blue = number, green = number })
  default     = { blue = 18443, green = 28443 }
}

variable "drain_seconds" {
  description = "How long a stopping server lets running requests finish (serve --drain-timeout). Its unit's stop timeout is a minute longer."
  type        = number
  default     = 600
}

variable "envoy_version" {
  description = "Envoy release (official linux-aarch_64 binary from github.com/envoyproxy/envoy). Pinned together with envoy_sha256."
  type        = string
  default     = "1.39.1"
}

variable "envoy_sha256" {
  description = "SHA-256 of envoy-<envoy_version>-linux-aarch_64. The bootstrap refuses a download that does not match."
  type        = string
  default     = "8565ad0af4b1d1d3c986e5165c027add3073579182f398dd7f4d728d25e9ec62"
}

variable "profiles" {
  description = "Subscription profile names, in fallback order (the first is preferred when quotas tie). Each gets one interactive login after the first apply. The service stays idle until every one has a login."
  type        = list(string)
  default     = ["claude-1", "claude-2", "claude-3"]
}

variable "release_tag" {
  description = "Release of ejc3/CLIProxyAPI that carries the claude-master-linux-arm64 binary. Pinned together with binary_sha256: change both to roll forward."
  type        = string
  default     = "claude-master-957ec56"
}

variable "binary_sha256" {
  description = "SHA-256 of the claude-master-linux-arm64 asset of release_tag. The bootstrap refuses a download that does not match."
  type        = string
  default     = "850334a53ad430ab06325c4fe6636286fa8f04b6ca69e70abfbc9909693ccd75"
}

variable "instance_type" {
  description = "Graviton instance type. The proxy is small; 1 GB with a swapfile is enough, and a 0.5 GB nano is not (its first boot was OOM-killed)."
  type        = string
  default     = "t4g.micro"
}

variable "enable_cloudwatch" {
  description = "Ship the proxy's OTLP metrics and its log file to CloudWatch through the CloudWatch agent. Metrics land in the namespace below."
  type        = bool
  default     = true
}

variable "metrics_namespace" {
  description = "CloudWatch namespace for the proxy's metrics."
  type        = string
  default     = "ClaudeMaster"
}

variable "log_retention_days" {
  description = "Retention of the proxy log group."
  type        = number
  default     = 90
}

variable "cwagent_version" {
  description = "CloudWatch agent version, pinned with its sha256 (arm64 .deb)."
  type        = string
  default     = "1.300073.2b1889"
}

variable "cwagent_sha256_arm64" {
  description = "SHA-256 of the pinned CloudWatch agent arm64 .deb."
  type        = string
  default     = "0d04b62f688f257aa35604f89b48f259cea5ed412985831d8d56838f43332169"
}

variable "backup_api_key_secret_id" {
  description = "Optional. Name or ARN of a Secrets Manager secret holding a paid Anthropic API key that the proxy uses ONLY after every subscription is out of quota. Empty means subscriptions only. The key is read at service start into the environment: never a command line, never a file."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Extra tags for every resource."
  type        = map(string)
  default     = {}
}
