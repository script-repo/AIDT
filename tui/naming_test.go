package main

import (
	"reflect"
	"testing"
)

func TestNextWorkerNamesUsesAIDTPrefix(t *testing.T) {
	got := nextWorkerNames("aidt-worker-04", 3)
	want := []string{"aidt-worker-04", "aidt-worker-05", "aidt-worker-06"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nextWorkerNames() = %v, want %v", got, want)
	}
}

func TestVMRoleRecognizesCurrentAndLegacyNames(t *testing.T) {
	tests := map[string]string{
		"aidt-gateway-01":  "gateway",
		"aidt-worker-01":   "worker",
		"olla-gateway-01":  "gateway",
		"ollama-worker-01": "worker",
	}
	for name, want := range tests {
		if got := vmRole(name); got != want {
			t.Errorf("vmRole(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDefaultWorkerModel(t *testing.T) {
	if DefaultModel != "nemotron-3-super:cloud" {
		t.Fatalf("DefaultModel = %q, want nemotron-3-super:cloud", DefaultModel)
	}
}
