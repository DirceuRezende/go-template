package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comentário\n\nDOTENV_A=1\nexport DOTENV_B = \"com espaço\"\nDOTENV_C='x=y'\nDOTENV_D=do-arquivo\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOTENV_D", "do-ambiente")
	for _, k := range []string{"DOTENV_A", "DOTENV_B", "DOTENV_C"} {
		t.Setenv(k, "") // registra restauração no fim do teste
		_ = os.Unsetenv(k)
	}

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"DOTENV_A": "1", "DOTENV_B": "com espaço", "DOTENV_C": "x=y", "DOTENV_D": "do-ambiente"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, quer %q", k, got, want)
		}
	}

	if err := loadDotEnv(filepath.Join(t.TempDir(), "nao-existe")); err != nil {
		t.Fatalf("arquivo ausente: %v", err)
	}
	if err := os.WriteFile(path, []byte("SEM_IGUAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(path); err == nil {
		t.Fatal("linha inválida deveria dar erro")
	}
}
