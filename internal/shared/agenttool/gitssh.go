package agenttool

// PathGitSSHKey gives a worker the control plane's SSH key for git (issue
// #625), when its Run's repository is registered with an SSH address. The
// node's git asks for it on each call through `hypomnema-node gitssh`, as
// Coder's `coder gitssh` asks its agent API (cli/gitssh.go, GitSSHKey).
const PathGitSSHKey = "/agent/v1/git/ssh-key"

// GitSSHKey answers PathGitSSHKey: the private half in OpenSSH's form, the
// public half in the authorized_keys form, and the keys of the repository's
// SSH host the owner confirmed, one "<type> <base64>" a line, which ssh
// trusts that host under and no other.
type GitSSHKey struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	HostKeys   string `json:"host_keys"`
}
