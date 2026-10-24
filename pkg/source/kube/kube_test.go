package kube

import (
	"errors"
	"testing"
)

func TestGuard(t *testing.T) {
	cases := []struct {
		ctx, host string
		ok        bool
	}{
		{"k3d-bee", "https://127.0.0.1:61954", true},
		{"k3d-bee", "https://0.0.0.0:6443", true},
		{"k3d-bee", "https://192.168.1.10:6443", false},
		{"k3d-bee", "https://localhost:6443", true},
		{"k3d-bee", "https://[::1]:6443", true},
		{"k3d-bee", "https://k8s.example.com", false},
		{"shared-prod", "https://127.0.0.1:6443", false},
		{"", "https://127.0.0.1:6443", false},
	}
	for _, c := range cases {
		err := guard(c.ctx, c.host)
		if (err == nil) != c.ok {
			t.Errorf("guard(%q, %q) = %v, want ok=%v", c.ctx, c.host, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrNotLocal) {
			t.Errorf("error does not wrap ErrNotLocal: %v", err)
		}
	}
}

func TestGroupOf(t *testing.T) {
	for in, want := range map[string]string{"bee-3": "bee", "bootnode-0": "bootnode", "light-node-1": "light-node", "solo": "solo"} {
		if got := groupOf(in); got != want {
			t.Errorf("groupOf(%q) = %q, want %q", in, got, want)
		}
	}
}
