package profilestore

import (
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// A profile may cap the memory of each of its workers in its YAML (issue #1313), written as a Kubernetes container's
// resources.limits.memory is:
//
//	limits:
//	  memory: 4Gi
//
// The node makes it the MemoryMax of the worker's own systemd scope, inside the slice that holds all its workers
// under the node's allocatable memory. Left out, a worker has no ceiling of its own, as a Kubernetes or Coder
// container's limits are optional (Coder examples/templates/kubernetes/main.tf:298-306): one build can take 2.7 GiB
// with a CLI beside it. Only memory is limited: CPU is elastic, and its overload slows a machine without hanging it.
type limitsSource struct {
	Memory string `yaml:"memory"`
}

// parse reads the limit; 0 is none. A memory request larger than the limit is refused, and a profile with a limit
// but no request asks for its limit, as Kubernetes validates a container's requests against its limits and copies a
// limit into a request left out (pkg/apis/core/validation/validation.go:6518; "Requests and limits" in Kubernetes'
// "Resource Management for Pods and Containers").
func (s limitsSource) parse(request int64) (memoryRequest, memoryLimit int64, err error) {
	m := strings.TrimSpace(s.Memory)
	if m == "" {
		return request, 0, nil
	}

	limit, err := ParseMemory(m)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: limits.memory %q is bytes, or a number with Ki, Mi, Gi, Ti, k, M, G or T, "+
			"more than 0", storeerr.ErrInvalid, m)
	}

	switch {
	case request == 0:
		request = limit
	case request > limit:
		return 0, 0, fmt.Errorf("%w: requests.memory (%d bytes) must be less than or equal to limits.memory (%d bytes)",
			storeerr.ErrInvalid, request, limit)
	}

	return request, limit, nil
}
