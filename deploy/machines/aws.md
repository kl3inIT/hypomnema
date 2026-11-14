# Machines at AWS

How the owner lets the control plane create EC2 instances as nodes (issue #69, step 3). What the control plane does
with them is in [ARCHITECTURE.md](../../ARCHITECTURE.md); this page is the setup.

Status: run for real on 2026-11-10 in `ap-southeast-1` with `t3.medium` machines, from the CLI and through the
client API. A machine installs itself and says hello about 70 seconds after it is asked for, with both CLIs found.
A stop takes about 30 seconds; after a start the node connects again by itself in about 20 seconds, with the same
token. `nodes rm` destroys the instance. Those runs used an administrator's key, so the narrow policy below has
not been proven yet.

The control plane must be reachable from the internet over `https`: a new machine is outside any private network,
and it downloads the install script from the control plane and then connects to it.

## 1. An IAM user that can only do this

Create an IAM user with an access key and this policy. Replace `PROVIDER_ID` with the id that
`hypomnema machines providers add` prints (`mpr_…`), so the key can destroy only instances this control plane made.
The policy follows the sample in Coder's `examples/templates/aws-linux/README.md`, narrowed to the calls made.

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
      "Sid": "StopStartAndDestroyOnlyItsOwn",
      "Effect": "Allow",
      "Action": ["ec2:TerminateInstances", "ec2:StopInstances", "ec2:StartInstances"],
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
hypomnema machines stop build-1                      # turns the instance off; its disk is kept and billed
hypomnema machines start build-1                     # turns it on; the node connects again by itself
hypomnema nodes rm build-1                           # destroys the instance, then forgets the node
```

A new machine is Ubuntu 24.04 with a `hypomnema` user (no sudo), git, and the Claude Code and Codex CLIs. A Claude
account the control plane keeps (`accounts add claude <label> --token`) and a git host's token reach it with each
Dispatch, so nothing is set for them. The Codex CLI's login is **not** delivered: set it on the machine once, as on
any node.

## 5. A machine that holds an Android emulator

```sh
hypomnema machines create phone --provider aws --device android   # c7i-flex.2xlarge unless --size names another
hypomnema nodes list                                               # "android device, kvm" once it says hello
```

The emulator needs KVM, which an EC2 instance that is not bare metal has only with nested virtualization: the size
must be of a family AWS lists for it (C7i, C7i-flex, C8i, C8id, C8i-flex, M7i, M7i-flex, M8i, M8id, M8i-flex, R7i,
R7iz, R8i, R8id, R8i-flex, X8i, I7i, I7ie; Intel only, no Graviton), and another is refused. The instance is
started with `NestedVirtualization=enabled`, which costs nothing more, and a disk of at least 40 GiB whatever
`disk_gib` says. Its first boot also installs Xvfb, Mesa and the emulator's libraries and puts the `hypomnema` user
in `kvm`; the Android SDK, the emulator and the phone image come in a later step of issue #417.

Cost in Singapore (`ap-southeast-1`), on demand, as tried in
[the research note](../../docs/research/2026-11-14-android-device-for-agents.md): about $0.39 an hour while it
runs, and about $3.84 a month for the 40 GB gp3 disk while it is stopped (`machines stop phone`).

`machines providers check` also lists instances that carry this provider's tag and that no node stands for. They
cost money and nothing here destroys them.
