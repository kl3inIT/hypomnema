package worktree

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Publish pushes the assigned branch's exact commit using the repository configured by the node.
// It returns no SHA for an up-to-date ref: observing an existing head is not proof of pushing it.
func Publish(ctx context.Context, src Source, path, taskID string) (string, error) {
	branch := "refs/heads/" + Branch(taskID)
	head, err := publishGit(ctx, path, nil, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(head)
	if _, err := hex.DecodeString(sha); len(sha) != 40 || err != nil {
		return "", fmt.Errorf("expected the branch's GitHub commit SHA")
	}
	objects, err := publishGit(ctx, path, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp("", "hyp-publish-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if _, err := publishGit(ctx, scratch, nil, "init", "--bare", "--template=", "--object-format=sha1"); err != nil {
		return "", err
	}
	env := append(append([]string{}, src.Env...), "GIT_ALTERNATE_OBJECT_DIRECTORIES="+strings.TrimSpace(objects))
	output, err := publishGit(ctx, scratch, env, "-c", "http.followRedirects=false",
		"push", "--porcelain", "--no-verify", "--no-follow-tags",
		"--recurse-submodules=no", "--", src.URL, sha+":"+branch)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(output, "\n") {
		flag, rest, _ := strings.Cut(line, "\t")
		refspec, _, _ := strings.Cut(rest, "\t")
		if refspec != sha+":"+branch {
			continue
		}
		switch flag {
		case "*", " ":
			return sha, nil
		case "=":
			return "", nil
		}
	}
	return "", fmt.Errorf("git did not confirm the branch update")
}

//nolint:gosec // the node fixes the commands, ref and remote
func publishGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git command while publishing: %w", err)
	}
	return string(out), nil
}
