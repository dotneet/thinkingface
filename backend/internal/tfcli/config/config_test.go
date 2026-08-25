package config

import (
	"os"
	"path/filepath"
	"testing"
)

func withConfigPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "config.json")
	t.Setenv("TF_CONFIG", p)
	return p
}

func TestPathPrecedence(t *testing.T) {
	t.Run("TF_CONFIG wins", func(t *testing.T) {
		t.Setenv("TF_CONFIG", "/tmp/explicit-config.json")
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		p, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if p != "/tmp/explicit-config.json" {
			t.Errorf("Path() = %q, want /tmp/explicit-config.json", p)
		}
	})

	t.Run("XDG_CONFIG_HOME used when TF_CONFIG unset", func(t *testing.T) {
		t.Setenv("TF_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
		p, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join("/tmp/xdg", "thinkingface", "config.json")
		if p != want {
			t.Errorf("Path() = %q, want %q", p, want)
		}
	})

	t.Run("falls back to home dir", func(t *testing.T) {
		t.Setenv("TF_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home dir available")
		}
		p, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".config", "thinkingface", "config.json")
		if p != want {
			t.Errorf("Path() = %q, want %q", p, want)
		}
	})
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	withConfigPath(t)
	f, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if f.DefaultEndpoint != "" {
		t.Errorf("DefaultEndpoint = %q, want empty", f.DefaultEndpoint)
	}
	if f.Credentials == nil {
		t.Errorf("Credentials = nil, want non-nil empty map")
	}
	if len(f.Credentials) != 0 {
		t.Errorf("Credentials = %v, want empty", f.Credentials)
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	p := withConfigPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() with malformed JSON: want error, got nil")
	}
}

func TestSaveLoadRoundTripAndPermissions(t *testing.T) {
	p := withConfigPath(t)

	f := &File{}
	f.Set(Credential{Endpoint: "https://tf.example.com", Token: "secret-token", Username: "alice"})

	if err := f.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat saved config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(p))
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 0700", perm)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.DefaultEndpoint != "https://tf.example.com" {
		t.Errorf("DefaultEndpoint = %q, want %q", loaded.DefaultEndpoint, "https://tf.example.com")
	}
	cred, ok := loaded.Get("https://tf.example.com")
	if !ok {
		t.Fatal("Get() missing credential after round trip")
	}
	if cred.Token != "secret-token" || cred.Username != "alice" {
		t.Errorf("credential = %+v, want token=secret-token username=alice", cred)
	}

	// No stray temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("config dir entries = %v, want just config.json", entries)
	}
}

// A thinkingface/ directory that already exists keeps whatever mode it was
// created with -- os.MkdirAll leaves an existing directory alone -- so Save
// has to tighten it itself.
func TestSaveTightensExistingLooseDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("TF_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir := filepath.Join(xdg, "thinkingface")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Mkdir applies the umask, so set the mode explicitly to be sure the
	// starting point really is world-readable.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	f := &File{}
	f.Set(Credential{Endpoint: "https://tf.example.com", Token: "secret-token"})
	if err := f.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 0700", perm)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat saved config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %o, want 0600", perm)
	}
}

// The counterpart: a directory named by TF_CONFIG belongs to the user, not to
// us, so Save must not narrow it even when it is world-readable.
func TestSaveLeavesTFConfigDirAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TF_CONFIG", filepath.Join(dir, "config.json"))

	f := &File{}
	f.Set(Credential{Endpoint: "https://tf.example.com", Token: "secret-token"})
	if err := f.Save(); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o755 {
		t.Errorf("config dir mode = %o, want it left at 0755", perm)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("stat saved config: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file mode = %o, want 0600", perm)
	}
}

func TestRemoveDefaultReassignment(t *testing.T) {
	f := &File{}
	f.Set(Credential{Endpoint: "https://a.example.com", Token: "a"})
	f.Set(Credential{Endpoint: "https://b.example.com", Token: "b"})
	// b is now default (Set makes the latest the default).

	f.Remove("https://b.example.com")
	if f.DefaultEndpoint != "https://a.example.com" {
		t.Errorf("DefaultEndpoint after removing default = %q, want reassigned to the only remaining endpoint", f.DefaultEndpoint)
	}

	f.Remove("https://a.example.com")
	if f.DefaultEndpoint != "" {
		t.Errorf("DefaultEndpoint after removing the last endpoint = %q, want empty", f.DefaultEndpoint)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "tf.example.com", want: "https://tf.example.com"},
		{in: "localhost:8080", want: "http://localhost:8080"},
		{in: "HTTP://X.Example.com/", want: "http://x.example.com"},
		{in: "https://a/b", wantErr: true},
		{in: "ftp://a", wantErr: true},
		{in: "  https://tf.example.com/  ", want: "https://tf.example.com"},
		{in: "https://tf.example.com?x=1", wantErr: true},
		{in: "https://tf.example.com#frag", wantErr: true},
		{in: "127.0.0.1:9000", want: "http://127.0.0.1:9000"},
		{in: "[::1]:9000", want: "http://[::1]:9000"},
		{in: "", wantErr: true},
		{in: "https://", wantErr: true},
		{in: "https://tf.example.com//", want: "https://tf.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := NormalizeEndpoint(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeEndpoint(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeEndpoint(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveEndpointPrecedence(t *testing.T) {
	file := &File{DefaultEndpoint: "https://config.example.com"}

	tests := []struct {
		name         string
		flagEndpoint string
		env          map[string]string
		file         *File
		wantEndpoint string
		wantSource   string
		wantErr      error
	}{
		{
			name:         "flag wins over everything",
			flagEndpoint: "flag.example.com",
			env: map[string]string{
				"TF_ENDPOINT":           "tf-env.example.com",
				"THINKINGFACE_ENDPOINT": "tface-env.example.com",
				"HF_ENDPOINT":           "hf-env.example.com",
			},
			file:         file,
			wantEndpoint: "https://flag.example.com",
			wantSource:   "flag",
		},
		{
			name: "TF_ENDPOINT beats THINKINGFACE_ENDPOINT and HF_ENDPOINT",
			env: map[string]string{
				"TF_ENDPOINT":           "tf-env.example.com",
				"THINKINGFACE_ENDPOINT": "tface-env.example.com",
				"HF_ENDPOINT":           "hf-env.example.com",
			},
			file:         file,
			wantEndpoint: "https://tf-env.example.com",
			wantSource:   "env TF_ENDPOINT",
		},
		{
			name: "THINKINGFACE_ENDPOINT beats HF_ENDPOINT",
			env: map[string]string{
				"THINKINGFACE_ENDPOINT": "tface-env.example.com",
				"HF_ENDPOINT":           "hf-env.example.com",
			},
			file:         file,
			wantEndpoint: "https://tface-env.example.com",
			wantSource:   "env THINKINGFACE_ENDPOINT",
		},
		{
			name:         "HF_ENDPOINT beats config",
			env:          map[string]string{"HF_ENDPOINT": "hf-env.example.com"},
			file:         file,
			wantEndpoint: "https://hf-env.example.com",
			wantSource:   "env HF_ENDPOINT",
		},
		{
			name:         "config is the last resort",
			file:         file,
			wantEndpoint: "https://config.example.com",
			wantSource:   "config",
		},
		{
			name:    "nothing set is an error",
			file:    nil,
			wantErr: ErrNoEndpoint,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Resolve(tt.flagEndpoint, "", envMap(tt.env), tt.file)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("Resolve() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(): %v", err)
			}
			if res.Endpoint != tt.wantEndpoint {
				t.Errorf("Endpoint = %q, want %q", res.Endpoint, tt.wantEndpoint)
			}
			if res.EndpointSource != tt.wantSource {
				t.Errorf("EndpointSource = %q, want %q", res.EndpointSource, tt.wantSource)
			}
		})
	}
}

func TestResolveTokenPrecedence(t *testing.T) {
	cfgFile := &File{DefaultEndpoint: "https://tf.example.com"}
	cfgFile.Set(Credential{Endpoint: "https://tf.example.com", Token: "config-token", Username: "alice"})

	t.Run("flag token wins", func(t *testing.T) {
		res, err := Resolve("", "flag-token", envMap(map[string]string{
			"TF_TOKEN": "tf-env-token", "HF_TOKEN": "hf-token", "HF_ENDPOINT": "https://tf.example.com",
		}), cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "flag-token" || res.TokenSource != "flag" {
			t.Errorf("got token=%q source=%q", res.Token, res.TokenSource)
		}
	})

	t.Run("THINKINGFACE_API_KEY beats TF_TOKEN, THINKINGFACE_TOKEN and config", func(t *testing.T) {
		env := envMap(map[string]string{
			"THINKINGFACE_API_KEY": "api-key-token", "TF_TOKEN": "tf-env-token", "THINKINGFACE_TOKEN": "tface-env-token",
		})
		res, err := Resolve("https://tf.example.com", "", env, cfgFile)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if res.Token != "api-key-token" || res.TokenSource != "env THINKINGFACE_API_KEY" {
			t.Fatalf("token = %q (%s), want api-key-token from env THINKINGFACE_API_KEY", res.Token, res.TokenSource)
		}
	})

	t.Run("TF_TOKEN beats THINKINGFACE_TOKEN and config", func(t *testing.T) {
		res, err := Resolve("", "", envMap(map[string]string{
			"TF_TOKEN": "tf-env-token", "THINKINGFACE_TOKEN": "tface-env-token",
		}), cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "tf-env-token" || res.TokenSource != "env TF_TOKEN" {
			t.Errorf("got token=%q source=%q", res.Token, res.TokenSource)
		}
	})

	t.Run("THINKINGFACE_TOKEN beats config", func(t *testing.T) {
		res, err := Resolve("", "", envMap(map[string]string{
			"THINKINGFACE_TOKEN": "tface-env-token",
		}), cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "tface-env-token" || res.TokenSource != "env THINKINGFACE_TOKEN" {
			t.Errorf("got token=%q source=%q", res.Token, res.TokenSource)
		}
	})

	t.Run("config beats HF_TOKEN even when HF_ENDPOINT matches", func(t *testing.T) {
		res, err := Resolve("", "", envMap(map[string]string{
			"HF_ENDPOINT": "https://tf.example.com", "HF_TOKEN": "hf-token",
		}), cfgFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "config-token" || res.TokenSource != "config" || res.Username != "alice" {
			t.Errorf("got token=%q source=%q username=%q", res.Token, res.TokenSource, res.Username)
		}
	})

	t.Run("HF_TOKEN used when no config credential and HF_ENDPOINT matches resolved endpoint", func(t *testing.T) {
		emptyFile := &File{DefaultEndpoint: "https://tf.example.com"}
		res, err := Resolve("", "", envMap(map[string]string{
			"HF_ENDPOINT": "https://tf.example.com", "HF_TOKEN": "hf-token",
		}), emptyFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "hf-token" || res.TokenSource != "env HF_TOKEN" {
			t.Errorf("got token=%q source=%q", res.Token, res.TokenSource)
		}
		if res.Username != "" {
			t.Errorf("Username = %q, want empty (HF_TOKEN is not a config credential)", res.Username)
		}
	})

	t.Run("HF_TOKEN not used when HF_ENDPOINT does not match resolved endpoint", func(t *testing.T) {
		emptyFile := &File{DefaultEndpoint: "https://tf.example.com"}
		// Pin the resolved endpoint via the flag so HF_ENDPOINT participates
		// only in the token safeguard check, not in endpoint resolution.
		res, err := Resolve("https://tf.example.com", "", envMap(map[string]string{
			"HF_ENDPOINT": "https://huggingface.co", "HF_TOKEN": "hf-token",
		}), emptyFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "" || res.TokenSource != "" {
			t.Errorf("got token=%q source=%q, want anonymous", res.Token, res.TokenSource)
		}
	})

	t.Run("anonymous when nothing matches", func(t *testing.T) {
		emptyFile := &File{DefaultEndpoint: "https://tf.example.com"}
		res, err := Resolve("", "", envMap(nil), emptyFile)
		if err != nil {
			t.Fatal(err)
		}
		if res.Token != "" || res.TokenSource != "" {
			t.Errorf("got token=%q source=%q, want anonymous", res.Token, res.TokenSource)
		}
	})
}
