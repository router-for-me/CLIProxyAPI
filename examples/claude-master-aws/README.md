# claude-master on AWS: example Terraform

A small, self-contained Terraform example that runs the claude-master **shared server** on one Graviton EC2 box:
a fixed private address, a security group that admits only your client networks, no SSH (Session Manager), an
encrypted root volume that survives `terraform destroy`, and optional CloudWatch metrics and logs.

The step-by-step guide, the operating notes and an overview of the OpenTelemetry metrics are in
[`docs/claude-master-aws.md`](../../docs/claude-master-aws.md).

```bash
cp terraform.tfvars.example terraform.tfvars    # vpc_id, subnet_id, private_ip, client_cidrs
terraform init && terraform apply
```

Then log each subscription in (interactive, once per profile), start the pool with `sudo claude-master-rollout`, and enrol your
client boxes: see the guide. Later restarts are rollouts too: Envoy in front of two servers, so no session notices.

| File | What it holds |
|---|---|
| `variables.tf` | Every input, with a description and a default where one is safe |
| `main.tf` | Security group, IAM, log group, instance |
| `user-data.sh.tftpl` | The bootstrap: service account, pinned binary, Envoy and the blue/green server units, rollout, CloudWatch agent, helpers |
| `outputs.tf` | Instance id, proxy address, the per-profile login commands |

Inputs without a default (`vpc_id`, `subnet_id`, `private_ip`, `client_cidrs`) must be set. The binary is pinned by
`release_tag` and `binary_sha256`; the bootstrap refuses a download that does not match.
