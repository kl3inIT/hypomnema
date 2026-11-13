# AWS as an Integration

The owner's AWS accounts are one Integration (issue #331). One key, the access key of an IAM user that may do
nothing but assume roles, is given to Hypomnema once; each role is a Connection with the rights its work needs. The
control plane assumes a role for 12 hours and renews it; the key and the sessions stay on the control plane's
machine. The machine provider takes its key from the role that makes machines.

Each step on AWS below changes the owner's account: it is done by the owner, or by an agent with the owner's yes for
that step. Replace `ACCOUNT` with the 12-digit account id.

## 1. The user that assumes roles

```sh
aws iam create-user --user-name hypomnema
aws iam put-user-policy --user-name hypomnema --policy-name assume-hypomnema-roles --policy-document '{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "sts:AssumeRole",
    "Resource": "arn:aws:iam::ACCOUNT:role/hypomnema/*"
  }]
}'
aws iam create-access-key --user-name hypomnema
```

The user can assume any role under the path `/hypomnema/`, so a role added later needs no change here. Keep the
access key's answer, or download the `.csv` the console gives, only until step 4.

## 2. A role

Every role trusts that user and allows 12-hour sessions:

```sh
cat > trust.json <<'EOF'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "AWS": "arn:aws:iam::ACCOUNT:user/hypomnema" },
    "Action": "sts:AssumeRole"
  }]
}
EOF
aws iam create-role --role-name hypomnema-read --path /hypomnema/ --max-session-duration 43200 \
  --assume-role-policy-document file://trust.json
```

What the role may do is what its policies allow; nothing in Hypomnema narrows it. A first role for agents that read
configuration and logs:

```sh
aws iam attach-role-policy --role-name hypomnema-read --policy-arn arn:aws:iam::aws:policy/job-function/ViewOnlyAccess
aws iam attach-role-policy --role-name hypomnema-read --policy-arn arn:aws:iam::aws:policy/CloudWatchReadOnlyAccess
```

A role for other work, such as restarting a service, is made the same way with the policy that work needs.

## 3. The role that makes machines

The machine provider's role has exactly the policy of [machines/aws.md](../machines/aws.md#1-an-iam-user-that-can-only-do-this):

```sh
aws iam create-role --role-name hypomnema-machines --path /hypomnema/ --max-session-duration 43200 \
  --assume-role-policy-document file://trust.json
aws iam put-role-policy --role-name hypomnema-machines --policy-name machines --policy-document file://machines.json
```

## 4. In Hypomnema

```sh
hypomnema integrations connect aws --label main-read --role arn:aws:iam::ACCOUNT:role/hypomnema/hypomnema-read \
  --key main --key-file hypomnema_accessKeys.csv
hypomnema integrations connect aws --label main-machines \
  --role arn:aws:iam::ACCOUNT:role/hypomnema/hypomnema-machines --key main
```

Each connect assumes the role once and says whether AWS let the key do it. The key file is the console's `.csv`, or
one line `ACCESS_KEY_ID:SECRET_ACCESS_KEY`; delete it afterwards. Another AWS account is another key name
(`--key other`), or the same key when that account's roles trust this user.

The machine provider moves to its role on the control plane's machine, then its old key is deleted:

```sh
hypomnema machines providers key aws --key aws:main-machines
hypomnema machines providers check aws
hypomnema secrets rm aws
```

Then deactivate and delete the old IAM user's access key at AWS.
