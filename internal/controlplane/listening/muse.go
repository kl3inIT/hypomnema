package listening

import (
	"context"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/muse"
)

// The Muse sources (issue #1292): Facebook, Instagram and Threads, read by asking the owner's Muse pool.

// The Muse sources' and backend's names.
const (
	SourceFacebook  = "facebook"
	SourceInstagram = "instagram"
	SourceThreads   = "threads"

	BackendMuse = "muse"
)

// Muse is the owner's Muse pool (muse.Service).
type Muse interface {
	ToolsOn(ctx context.Context) (bool, error)
	Ask(ctx context.Context, q muse.Question) (muse.Result, error)
}

// MuseSources are Facebook, Instagram and Threads, each read through Muse; client opens the links Muse gives.
func MuseSources(m Muse, client *http.Client) []Platform {
	_, _ = m, client

	var out []Platform

	for _, name := range []string{SourceFacebook, SourceInstagram, SourceThreads} {
		out = append(out, Platform{Name: name, Backends: []Backend{{
			Name: BackendMuse, Host: "muse.ai",
			Search: func(context.Context, Request) (Result, error) { return Result{}, Fail(CodeBroken, "not built") },
		}}})
	}

	return out
}
