---
name: aws
description: >-
  Works in the owner's AWS accounts with the aws command (AWS's own CLI), which Hypomnema installs and signs with
  the role the owner gave you: reads logs in CloudWatch, describes servers, databases and load balancers, reads
  metrics, and does whatever else the role allows. Use when the owner asks about something running on AWS: why a
  service failed, what its logs say, what is deployed, how a server is doing. Not for creating Hypomnema's own
  machines, which the control plane does itself.
---

# AWS through Hypomnema

Work in AWS with the `aws` command, AWS's own CLI version 2. Hypomnema installed it for you. Every request it makes
goes through the control plane, which signs it with the session of the role the owner gave you: you never see a key
or a session, and you never log in. Written for Hypomnema (issue #331).

## How it is signed in

- Run `aws` as you would anywhere. A small launcher runs the real command with placeholder credentials; the control
  plane drops their signature and signs each request again with your role's session.
- **Never run `aws configure`, `aws sso login`, `aws login` or `aws sts assume-role`**, never set
  `AWS_ACCESS_KEY_ID`, `AWS_PROFILE` or a credentials file, and never print credentials. `sts assume-role`,
  `sts get-session-token`, `iam create-access-key` and the other calls that make credentials are always refused.
- `aws sts get-caller-identity` says which account and role you act as.
- A command `aws` signs in chunks (some large S3 uploads) cannot be signed again and is refused; say so.

## Which role

- Each role is an account of its own to Hypomnema, with a label such as `main-read`. When you were given one, every
  command uses it.
- When you were given several, name one on each command: `aws --account main-read logs tail ...`. The label is the
  owner's; a command that names none is refused with the list.
- What a role may do is what its policies at AWS allow. An `AccessDenied` (or `UnauthorizedOperation`) answer means
  the role does not have that right: report the action and the resource it named. Do not look for another role or
  another way; the owner decides whether the role gets it.

## Always

- Name the region on every command (`--region ap-southeast-1`); nothing sets one for you.
- Ask for JSON and cut it down: `--output json --query '...'`. Paginated lists come whole unless you pass
  `--max-items`.
- Times in CloudWatch are milliseconds since 1970 in UTC; say the time zone when you report one.

## Reading what is running

```bash
aws sts get-caller-identity
aws ec2 describe-instances --region R --query 'Reservations[].Instances[].[InstanceId,State.Name,Tags[?Key==`Name`]|[0].Value]' --output table
aws logs describe-log-groups --region R --query 'logGroups[].logGroupName'
aws logs tail <group> --region R --since 1h
aws logs tail <group> --region R --since 1h --filter-pattern ERROR
aws logs filter-log-events --log-group-name <group> --region R --start-time <ms> --filter-pattern '"Exception"'
aws cloudwatch get-metric-statistics --region R --namespace AWS/EC2 --metric-name CPUUtilization \
  --dimensions Name=InstanceId,Value=<id> --start-time <iso> --end-time <iso> --period 300 --statistics Average
aws elbv2 describe-target-health --region R --target-group-arn <arn>
aws rds describe-db-instances --region R --query 'DBInstances[].[DBInstanceIdentifier,DBInstanceStatus]'
```

`aws logs tail --follow` does not end; never use it. A server's own files and services are not reachable this way:
only what reaches CloudWatch or AWS's APIs. When logs are not in CloudWatch, say so.

## Changes

- A change runs at once when the role allows it, unless the owner set that operation to ask: then the command
  answers that it waits for the owner's approval, and was not run. Tell the owner, and run the same command again,
  unchanged, once it is approved.
- Before a change that stops, deletes, replaces or restarts something, say in your report what it is and why, and
  check with the owner first unless the owner already asked for exactly that change.
- Report what you changed, with the command and the resource's id.
