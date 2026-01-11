package agent

import (
	"github.com/angariumd/angarium/internal/models"
)

type GPUProvider interface {
	GetGPUs() ([]models.GPU, error)
}

type MockGPUProvider struct {
	GPUs []models.GPU
}

func (f *MockGPUProvider) GetGPUs() ([]models.GPU, error) {
	for i := range f.GPUs {
		// Mock bit of usage
		f.GPUs[i].Utilization = (i + 1) * 10
		f.GPUs[i].MemoryUsedMB = (i + 1) * 512
	}
	return f.GPUs, nil
}

func NewMockGPUProvider(nodeID string) *MockGPUProvider {
	return &MockGPUProvider{
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
