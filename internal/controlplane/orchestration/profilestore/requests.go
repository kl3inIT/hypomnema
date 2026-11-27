package profilestore

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// A profile asks for CPU and memory in its YAML (issue #731), written as a
// Kubernetes container's requests are, resources.requests.cpu and .memory
// (apimachinery's resource.Quantity):
//
//	requests:
//	  cpu: 500m      # or 0.5: half a core
//	  memory: 512Mi  # or Gi, Ki, M, G, k, or bytes
//
// Left out, each is the default for building work (DefaultCPURequestMillis,
// DefaultMemoryRequest).
type requestsSource struct {
	CPU    string `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

// maxRequestMillis and maxRequestBytes bound a request to what one machine
// could have (inferred), so a typo is refused rather than never placed.
const (
	maxRequestMillis = 1024 * 1000
	maxRequestBytes  = 16 << 40
)

// parse reads the requests; 0 is a part left out.
func (s requestsSource) parse() (cpuMillis, memoryBytes int64, err error) {
	if c := strings.TrimSpace(s.CPU); c != "" {
		if cpuMillis, err = ParseCPU(c); err != nil {
			return 0, 0, err
		}
	}

	if m := strings.TrimSpace(s.Memory); m != "" {
		if memoryBytes, err = ParseMemory(m); err != nil {
			return 0, 0, err
		}
	}

	return cpuMillis, memoryBytes, nil
}

// ParseCPU reads cores, "0.5" or "2", or thousandths of a core, "500m".
func ParseCPU(s string) (int64, error) {
	bad := fmt.Errorf("%w: requests.cpu %q is cores (0.5) or millicores (500m), more than 0", storeerr.ErrInvalid, s)

	if n, ok := strings.CutSuffix(s, "m"); ok {
		v, err := strconv.ParseInt(n, 10, 64)
		if err != nil || v <= 0 || v > maxRequestMillis {
			return 0, bad
		}

		return v, nil
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || v <= 0 || v*1000 > maxRequestMillis {
		return 0, bad
	}

	millis := int64(math.Round(v * 1000))
	if millis <= 0 {
		return 0, bad
	}

	return millis, nil
}

// memoryUnits are the suffixes of a Kubernetes quantity a memory request may take.
var memoryUnits = []struct {
	suffix string
	bytes  int64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
	{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
}

// ParseMemory reads bytes, "536870912", or a number and a unit, "512Mi", "3Gi", "1.5G".
func ParseMemory(s string) (int64, error) {
	bad := fmt.Errorf("%w: requests.memory %q is bytes, or a number with Ki, Mi, Gi, Ti, k, M, G or T, more than 0",
		storeerr.ErrInvalid, s)

	number, unit := s, int64(1)

	for _, u := range memoryUnits {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			number, unit = n, u.bytes

			break
		}
	}

	v, err := strconv.ParseFloat(number, 64)
	if err != nil || math.IsNaN(v) || v <= 0 || v*float64(unit) > maxRequestBytes {
		return 0, bad
	}

	bytes := int64(math.Ceil(v * float64(unit)))

	return bytes, nil
}
