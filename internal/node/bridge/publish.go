package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnablePublish lets the worker's tools offer a publish request with no worker-supplied data.
func EnablePublish(dir string) error { return os.MkdirAll(filepath.Join(dir, "publish"), 0o700) }

// PublishEnabled reports whether a worker was offered the node's push tool.
func PublishEnabled(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "publish"))
	return err == nil && info.IsDir()
}

// RequestPublish asks the node to publish the assigned branch and returns the request id.
func RequestPublish(dir string) (string, error) {
	f, err := os.CreateTemp(filepath.Join(dir, "publish"), "request-")
	if err != nil {
		return "", err
	}
	return filepath.Base(f.Name()), f.Close()
}

// PublishRequests lists the worker's pending requests; their contents are never read.
func PublishRequests(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "publish"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var requests []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "request-") && entry.Type().IsRegular() {
			requests = append(requests, entry.Name())
		}
	}
	return requests, nil
}

// CompletePublish records what the node did before it clears the request.
func CompletePublish(dir, id string, result PublishResult) error {
	if err := writeFile(filepath.Join(dir, "published"), id, result); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, "publish", id))
}

// PublishResult is a tool reply, not evidence used by the control plane.
type PublishResult struct {
	Message string `json:"message"`
	Failed  bool   `json:"failed,omitempty"`
}

// Published reads the node's reply, if available.
func Published(dir, id string) (PublishResult, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, "published", id)) //nolint:gosec // id is a bridge request filename
	if errors.Is(err, os.ErrNotExist) {
		return PublishResult{}, false, nil
	}
	if err != nil {
		return PublishResult{}, false, err
	}
	var result PublishResult
	err = json.Unmarshal(data, &result)
	return result, err == nil, err
}
