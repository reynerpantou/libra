package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nLIBRA_T_A=plain\nexport LIBRA_T_B=\"quoted value\"\nLIBRA_T_C='single'\r\nLIBRA_T_D=\nLIBRA_T_E=from-file\nnot a setting\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIBRA_T_E", "from-env")
	for _, k := range []string{"LIBRA_T_A", "LIBRA_T_B", "LIBRA_T_C", "LIBRA_T_D"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	LoadDotEnv(path)
	want := map[string]string{"LIBRA_T_A": "plain", "LIBRA_T_B": "quoted value", "LIBRA_T_C": "single", "LIBRA_T_D": "", "LIBRA_T_E": "from-env"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	LoadDotEnv(filepath.Join(t.TempDir(), "missing")) // no file: no-op
}
