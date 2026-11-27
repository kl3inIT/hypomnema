package orchestration

import (
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/profilestore"
)

// ParseReserve reads what the owner keeps back from a node's capacity, in the
// same notation as a profile's requests (issue #731): cpu in cores or
// millicores, memory in bytes or with a unit; "" or "0" is none.
func ParseReserve(cpu, memory string) (Resources, error) {
	var (
		r   Resources
		err error
	)

	if c := strings.TrimSpace(cpu); c != "" && c != "0" {
		if r.CPUMillis, err = profilestore.ParseCPU(c); err != nil {
			return Resources{}, err
		}
	}

	if m := strings.TrimSpace(memory); m != "" && m != "0" {
		if r.MemoryBytes, err = profilestore.ParseMemory(m); err != nil {
			return Resources{}, err
		}
	}

	return r, nil
}
