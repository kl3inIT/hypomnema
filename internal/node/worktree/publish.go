package worktree

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Publish pushes the exact commit of the Task's branch to target, the Task's own branch or the one of the Task it
// continues (issue #690), using the repository configured by the node. It returns no SHA for an up-to-date ref:
// observing an existing head is not proof of pushing it. The push is never forced, as t3code's
// (GitVcsDriverCore.ts:3545) and Orca's without the user's explicit ask (remote.ts:23-48): when target moved on the
// host, the error says to merge it first.
func Publish(ctx context.Context, src Source, path, taskID, target string) (string, error) {
	if target == "" {
		target = Branch(taskID)
	}
	head, err := publishGit(ctx, path, nil, "rev-parse", "--verify", "refs/heads/"+Branch(taskID)+"^{commit}")
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
	env := append(append([]string{}, src.Env...),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+strings.TrimSpace(objects), "LC_ALL=C")
	branch := "refs/heads/" + target
	output, err := publishGit(ctx, scratch, env, "-c", "http.followRedirects=false",
		"push", "--porcelain", "--no-verify", "--no-follow-tags",
		"--recurse-submodules=no", "--", src.URL, sha+":"+branch)
	if err != nil {
		if nonFastForward(output, sha+":"+branch) {
			return "", fmt.Errorf("the host refused the push because %s has commits your branch does not have "+
				"(it moved meanwhile): run `git fetch origin %s && git merge FETCH_HEAD`, resolve and commit any "+
				"conflicts, then publish again; the push is never forced", target, branch)
		}
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

func nonFastForward(output, refspec string) bool {
	for line := range strings.SplitSeq(output, "\n") {
		flag, rest, _ := strings.Cut(line, "\t")
		got, reason, _ := strings.Cut(rest, "\t")
		if flag == "!" && got == refspec {
			return reason == "[rejected] (non-fast-forward)" || reason == "[rejected] (fetch first)"
		}
	}
	return false
}

// publishGit returns git's standard output, also when it fails: a refused push says why there.
//
//nolint:gosec // the node fixes the commands, ref and remote
func publishGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("git command while publishing: %w", err)
	}
	return string(out), nil
}
