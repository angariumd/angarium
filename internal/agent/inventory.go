package agent

import (
	"math/rand"
	"time"

	"github.com/angariumd/angarium/internal/models"
)

type GPUProvider interface {
	GetGPUs() ([]models.GPU, error)
}

type MockGPUProvider struct {
	GPUs []models.GPU
	rng  *rand.Rand
}

func (f *MockGPUProvider) GetGPUs() ([]models.GPU, error) {
	for i := range f.GPUs {
		// Mock bit of usage with jitter
		f.GPUs[i].Utilization = 10 + f.rng.Intn(40)     // 10-50%
		f.GPUs[i].MemoryUsedMB = 512 + f.rng.Intn(2048) // 512MB-2.5GB
	}
	return f.GPUs, nil
}

func NewMockGPUProvider(nodeID string) *MockGPUProvider {
	return &MockGPUProvider{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
		GPUs: []models.GPU{
			{
				UUID:     "GPU-mock-01",
				Idx:      0,
				Name:     "NVIDIA A100-SXM4-40GB (Mock)",
				MemoryMB: 40960,
				Health:   "OK",
			},
			{
				UUID:     "GPU-mock-02",
				Idx:      1,
				Name:     "NVIDIA A100-SXM4-40GB (Mock)",
				MemoryMB: 40960,
				Health:   "OK",
			},
		},
	}
}
