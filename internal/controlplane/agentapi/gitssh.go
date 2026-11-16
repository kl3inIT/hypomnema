package agentapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// GitSSHKey is the control plane's SSH key for git (issue #625): its private
// half in OpenSSH's form and its public half. It is the control plane's own
// wiring over the vault: this package never opens a secret.
type GitSSHKey func(ctx context.Context) (private, public string, err error)

// WithGitSSHKey gives workers the control plane's SSH key for git. Without
// it the route answers that there is none.
func (s *Server) WithGitSSHKey(k GitSSHKey) *Server {
	s.gitSSHKey = k

	return s
}

// gitSSHKeyFor gives the key to a worker whose Run's repository is
// registered with an SSH address, and to no other agent: as the host's token
// goes only with the offer of a Dispatch on that host (nodeapi), the key goes
// only to a Dispatch whose git clones over SSH. A coordinator, a Bot, a
// Routine's script and the owner's token never get it: none of them clones,
// and the owner reads the vault himself. As Coder's agent API gives a
// workspace's agent its owner's key (coderd/gitsshkey.go, agentGitSSHKey),
// asked by `coder gitssh` on each use.
func (s *Server) gitSSHKeyFor(w http.ResponseWriter, r *http.Request) {
	a, err := s.authenticate(r)
	if err != nil {
		refuse(w, http.StatusUnauthorized, err.Error())

		return
	}

	if s.gitSSHKey == nil {
		refuse(w, http.StatusServiceUnavailable, "this control plane keeps no SSH key for git")

		return
	}

	if a.dispatchID == "" {
		refuse(w, http.StatusForbidden, "only a worker whose repository is cloned over SSH gets the SSH key")

		return
	}

	hostKeys, overSSH, err := s.store.SSHOfDispatch(r.Context(), a.dispatchID)
	if err != nil {
		s.fail(w, r, err)

		return
	}

	if !overSSH {
		refuse(w, http.StatusForbidden, "this worker's repository is not registered with an SSH address")

		return
	}

	private, public, err := s.gitSSHKey(r.Context())
	if err != nil {
		s.fail(w, r, err)

		return
	}

	// Who got the key, and never the key.
	s.logger.InfoContext(r.Context(), "an agent got the SSH key for git",
		slog.String("event", "agentapi.git_ssh_key"), slog.String("agent", a.name))

	w.Header().Set("Cache-Control", "no-store")
	answer(w, agenttool.GitSSHKey{PrivateKey: private, PublicKey: public, HostKeys: hostKeys})
}
