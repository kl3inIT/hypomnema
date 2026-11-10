# Machines at AWS

How the owner lets the control plane create EC2 instances as nodes (issue #69, step 3). What the control plane does
with them is in [ARCHITECTURE.md](../../ARCHITECTURE.md); this page is the setup.

Status: the adapter is tested against a stand-in for EC2. It has **not yet created a real instance**; the policy
below and the first-boot script are checked on the first real run.

## 1. An IAM user that can only do this

Create an IAM user with an access key and this policy. Replace `PROVIDER_ID` with the id that
`hypomnema machines providers add` prints (`mpr_…`), so the key can destroy only instances this control plane made.
The policy follows the sample in Coder's `examples/templates/aws-linux/README.md`, narrowed to the five calls made.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Read",
      "Effect": "Allow",
      "Action": ["ec2:DescribeInstances", "ec2:DescribeInstanceTypes", "ec2:DescribeImages"],
      "Resource": "*"
    },
    { "Sid": "Create", "Effect": "Allow", "Action": "ec2:RunInstances", "Resource": "*" },
    {
      "Sid": "TagWhatItCreates",
      "Effect": "Allow",
      "Action": "ec2:CreateTags",
      "Resource": ["arn:aws:ec2:*:*:instance/*", "arn:aws:ec2:*:*:volume/*"],
      "Condition": { "StringEquals": { "ec2:CreateAction": "RunInstances" } }
    },
    {
      "Sid": "DestroyOnlyItsOwn",
      "Effect": "Allow",
      "Action": "ec2:TerminateInstances",
      "Resource": "arn:aws:ec2:*:*:instance/*",
      "Condition": { "StringEquals": { "aws:ResourceTag/hypomnema-provider": "PROVIDER_ID" } }
    }
  ]
}
```

## 2. The key in the vault

The secret's value is the access key id and the secret access key joined by a colon, read from a file or standard
input and never printed:

```sh
printf '%s:%s' "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" | hypomnema secrets add aws --from-file -
```

## 3. The provider

```sh
hypomnema machines providers add aws --kind aws --key vault:aws --region ap-southeast-1 --size t3.medium --max 3
hypomnema machines providers check aws
```

`--max` is how many machines may exist at once. Options, each as `--option name=value`:

| Option | Meaning | Default |
|---|---|---|
| `subnet_id` | The subnet instances start in. It must give them a way out to the internet. | The default VPC's |
| `security_group_ids` | Comma-separated security groups. No inbound rule is needed: the node dials out. | The VPC's default group |
| `key_name` | An EC2 key pair, to reach the machine over SSH (with a group that allows it). | None |
| `disk_gib` | The root volume's size. | 40 |

`HYPOMNEMA_PUBLIC_URL` must be the control plane's `https` URL: a new machine installs the node from it.

## 4. Machines

```sh
hypomnema machines create build-1 --provider aws     # region and size as the provider's, or --region, --size
hypomnema nodes list                                 # provisioning, then connected
hypomnema nodes rm build-1                           # destroys the instance, then forgets the node
```

A new machine is Ubuntu 24.04 with a `hypomnema` user (no sudo), git, and the Claude Code and Codex CLIs. Their
logins and git's credentials are **not** on it: until the vault delivers them, set them on the machine once, as on
any node.

`machines providers check` also lists instances that carry this provider's tag and that no node stands for. They
cost money and nothing here destroys them.
