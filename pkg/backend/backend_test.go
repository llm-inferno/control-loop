package backend

import (
	"os"
	"testing"

	ctrl "github.com/llm-inferno/control-loop/pkg/controller"
)

func TestModeFromEnv(t *testing.T) {
	cases := map[string]Mode{
		"":          ModeServerSim,
		"serversim": ModeServerSim,
		"llmd":      ModeLLMD,
		"llm-d":     ModeLLMD,
		"LLMD":      ModeLLMD,
		"bogus":     ModeServerSim,
	}
	for in, want := range cases {
		t.Setenv(ctrl.BackendEnvName, in)
		if in == "" {
			os.Unsetenv(ctrl.BackendEnvName)
		}
		if got := ModeFromEnv(); got != want {
			t.Errorf("ModeFromEnv(%q) = %q, want %q", in, got, want)
		}
	}
}
