package agent

import (
	"bufio"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"
)

type SystemMetrics struct {
	TotalMB int
	UsedMB  int
}

func GetSystemMetrics() SystemMetrics {
	metrics := SystemMetrics{}

	f, err := os.Open("/proc/meminfo")
	if err == nil {
		defer f.Close()
		var memTotal, memAvailable int64
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}

			key := strings.TrimSuffix(parts[0], ":")
			val, _ := strconv.ParseInt(parts[1], 10, 64)

			switch key {
			case "MemTotal":
				memTotal = val
			case "MemAvailable":
				memAvailable = val
			}
		}

		// /proc/meminfo is in kB
		metrics.TotalMB = int(memTotal / 1024)
		if memTotal > memAvailable {
			metrics.UsedMB = int((memTotal - memAvailable) / 1024)
		}

		if metrics.TotalMB > 0 {
			return metrics
		}
	}

	// Simulated mode
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	return SystemMetrics{
		TotalMB: 65536,
		UsedMB:  8192 + rng.Intn(16384), // 8GB - 24GB
	}
}
