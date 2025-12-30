package agent

import (
	"github.com/angariumd/angarium/internal/models"
)

type GPUProvider interface {
	GetGPUs() ([]models.GPU, error)
}

type FakeGPUProvider struct {
	GPUs []models.GPU
}

func (f *FakeGPUProvider) GetGPUs() ([]models.GPU, error) {
	return f.GPUs, nil
}

func NewFakeGPUProvider(nodeID string) *FakeGPUProvider {
	return &FakeGPUProvider{
		GPUs: []models.GPU{
			{
				UUID:     "GPU-fake-01",
				Idx:      0,
				Name:     "NVIDIA A100-SXM4-40GB (Fake)",
				MemoryMB: 40960,
				Health:   "OK",
			},
			{
				UUID:     "GPU-fake-02",
				Idx:      1,
				Name:     "NVIDIA A100-SXM4-40GB (Fake)",
				MemoryMB: 40960,
				Health:   "OK",
			},
		},
	}
}
