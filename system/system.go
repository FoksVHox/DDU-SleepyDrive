package system

import (
	"runtime"
)

type Information struct {
	Version       string `json:"version"`
	KernelVersion string `json:"kernel_version"`
	Architecture  string `json:"architecture"`
	OS            string `json:"os"`
	CpuCount      int    `json:"cpu_count"`
}

func GetSystemInformation() (*Information, error) {

	s := &Information{
		Version:      "v1",
		Architecture: runtime.GOARCH,
		OS:           runtime.GOOS,
		CpuCount:     runtime.NumCPU(),
	}

	return s, nil
}
