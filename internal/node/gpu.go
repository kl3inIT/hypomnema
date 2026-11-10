package node

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// GPUs are read as Beszel's agent reads them (agent/gpu.go, MIT, Copyright
// (c) 2024 henrygd, https://github.com/henrygd/beszel): nvidia-smi is asked
// for the same fields in the same CSV form and parsed the same way. Changed:
// it is run once for each heartbeat instead of left running with -l, since a
// node measures at its heartbeat and averages nothing; only NVIDIA is read.

const (
	nvidiaSmi = "nvidia-smi"
	// gpuReadTimeout bounds one nvidia-smi run, so a hung driver cannot hold a heartbeat.
	gpuReadTimeout = 3 * time.Second
	// maxGPUs bounds what one machine reports.
	maxGPUs = 16

	mebibyte = 1 << 20
)

// nvidiaSmiArgs is Beszel's query, without its -l interval.
var nvidiaSmiArgs = []string{
	"--query-gpu=index,name,temperature.gpu,memory.used,memory.total,utilization.gpu,power.draw",
	"--format=csv,noheader,nounits",
}

// nvidiaSmiPath finds nvidia-smi once: a machine does not grow a GPU while
// the node runs, and a machine without one must not pay a lookup at every
// heartbeat.
var nvidiaSmiPath = sync.OnceValue(func() string {
	path, err := exec.LookPath(nvidiaSmi)
	if err != nil {
		return ""
	}

	return path
})

// MeasureGPUs is the machine's GPUs now; nil when it has none that can be
// read.
func MeasureGPUs(ctx context.Context) []nodeproto.GPUStats {
	path := nvidiaSmiPath()
	if path == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, gpuReadTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, nvidiaSmiArgs...).Output() //nolint:gosec // G204: the nvidia-smi found on PATH, fixed arguments
	if err != nil {
		return nil
	}

	return parseNvidiaSmi(out)
}

// parseNvidiaSmi reads nvidia-smi's CSV: one line a GPU, with its index,
// name, temperature in °C, memory used and total in MiB, use in percent and
// power in watts. A value nvidia-smi does not know ("[N/A]") is left out; a
// line that is not a GPU's is skipped.
func parseNvidiaSmi(out []byte) []nodeproto.GPUStats {
	var gpus []nodeproto.GPUStats

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() && len(gpus) < maxGPUs {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ", ")
		if len(fields) < 7 {
			continue
		}

		index, err := strconv.Atoi(fields[0])
		used, usedOK := number(fields[3])
		total, totalOK := number(fields[4])
		usage, usageOK := number(fields[5])

		if err != nil || !usedOK || !totalOK || !usageOK {
			continue
		}

		// The name without what every card of the vendor says, as Beszel shortens it.
		name := strings.TrimSuffix(strings.TrimPrefix(fields[1], "NVIDIA "), " Laptop GPU")

		gpu := nodeproto.GPUStats{
			Index: index, Name: name, UsagePercent: min(max(usage, 0), 100),
			MemoryUsedBytes: int64(used * mebibyte), MemoryTotalBytes: int64(total * mebibyte),
		}

		if temp, ok := number(fields[2]); ok {
			gpu.TemperatureCelsius = &temp
		}

		if power, ok := number(fields[6]); ok {
			gpu.PowerWatts = &power
		}

		gpus = append(gpus, gpu)
	}

	return gpus
}

// number parses a value nvidia-smi printed; it reports false for "[N/A]" and
// anything else that is not a number or is below zero.
func number(field string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)

	return v, err == nil && v >= 0
}
