---
name: aws
description: >-
  Works in the owner's AWS accounts with the aws command (AWS's own CLI), which Hypomnema installs and signs with
  the role the owner gave you: reads logs in CloudWatch, describes servers, databases and load balancers, reads
  metrics, and does whatever else the role allows. Use when the owner asks about something running on AWS: why a
  service failed, what its logs say, what is deployed, how a server is doing. Not for creating Hypomnema's own
  machines, which the control plane does itself.
---

# AWS

This stub loads the guide that ships with the `hypomnema` binary on this machine, so the guide matches the `aws`
command that binary set up.

1. Run `hypomnema skills get aws` once in the session and follow the guide it prints.

When `aws` says you were given no AWS role, tell the owner: a role is connected in Settings, Integrations, and
switched on there for chats or given to the Run.
