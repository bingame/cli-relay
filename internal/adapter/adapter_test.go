package adapter_test

import (
	"relay/internal/adapter"
	"relay/internal/adapter/mock"
	"relay/internal/provider"
	"testing"
)

func TestMockLaunchContract(t *testing.T) {
	var a adapter.LaunchAdapter = &mock.Adapter{Name: "test", Inputs: adapter.LaunchInputs{Binary: "fake", Env: map[string]string{"SECRET": "dummy"}}, Resume: []string{"resume"}}
	artifact, err := a.Render(provider.Provider{ID: "test"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := a.BuildLaunchInputs(artifact, nil)
	if err != nil || inputs.Binary != "fake" || len(inputs.Args) != 0 {
		t.Fatalf("契约不符: %v", err)
	}
	if a.ResumeArgs("session")[1] != "session" {
		t.Fatal("恢复参数丢失")
	}
}
