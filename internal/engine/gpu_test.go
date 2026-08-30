package engine

import (
	"errors"
	"testing"
)

func TestIsCUDAFailure(t *testing.T) {
	for _, message := range []string{
		"ggml_cuda_init: failed to initialize CUDA",
		"cuBLAS backend unavailable",
		"GPU backend initialization failed",
		"driver/library version mismatch",
	} {
		if !isCUDAFailure(errors.New(message)) {
			t.Errorf("did not recognize CUDA failure: %s", message)
		}
	}
	if isCUDAFailure(errors.New("invalid subtitle output path")) {
		t.Fatal("ordinary processing error was classified as CUDA failure")
	}
}
