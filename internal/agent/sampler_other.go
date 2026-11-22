//go:build !windows

package agent

import "errors"

// NewSampler returns the platform sampler. Tracking is Windows-only.
func NewSampler() Sampler {
	return SamplerFunc(func() (Sample, error) {
		return Sample{}, errors.New("activity tracking is only supported on Windows")
	})
}
