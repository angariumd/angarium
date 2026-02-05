package agent

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

type NvidiaGPUProvider struct{}

func (p *NvidiaGPUProvider) GetGPUs() ([]models.GPU, error) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=index,gpu_uuid,name,memory.total", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("calling nvidia-smi: %w", err)
	}

	r := csv.NewReader(strings.NewReader(string(output)))
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing nvidia-smi output: %w", err)
	}

	var gpus []models.GPU
	now := time.Now()

	for _, record := range records {
		if len(record) < 4 {
			continue
		}

		idx, _ := strconv.Atoi(record[0])
		uuid := record[1]

		name := record[2]
		memTotal, _ := strconv.Atoi(record[3])

		gpus = append(gpus, models.GPU{
			ID:         "", // Will be populated by controller or derived
			Idx:        idx,
			UUID:       uuid,
			Name:       name,
			MemoryMB:   memTotal,
			Health:     "OK", // Simple for MVP
			LastSeenAt: now,
		})
	}

	return gpus, nil
}
