package tfcli

import (
	"path/filepath"
	"testing"
)

// isolateEnv points TF_CONFIG at a private, empty file for the duration of
// the test and clears every environment variable config.Resolve consults, so
// tests never read or write the developer's real ~/.config/thinkingface.
func isolateEnv(t *testing.T) (configPath string) {
	t.Helper()
	configPath = filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TF_CONFIG", configPath)
	for _, k := range []string{
		"TF_ENDPOINT", "TF_TOKEN",
		"THINKINGFACE_ENDPOINT", "THINKINGFACE_TOKEN", "THINKINGFACE_API_KEY",
		"HF_ENDPOINT", "HF_TOKEN",
	} {
		t.Setenv(k, "")
	}
	return configPath
}
