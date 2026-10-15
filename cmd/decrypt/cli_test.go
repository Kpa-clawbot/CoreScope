package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the real flag/storage/query/render path without building another binary.
func TestDecryptCLIProcess(t *testing.T) {
	if os.Getenv("CORESCOPE_DECRYPT_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"corescope-decrypt"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("corescope-decrypt", flag.ExitOnError)
	main()
	os.Exit(0)
}

func runDecryptCLI(t *testing.T, base string, args ...string) ([]byte, string, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, append([]string{"-test.run=^TestDecryptCLIProcess$", "--"}, args...)...)
	cmd.Dir = base
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "DB_PATH", "CORESCOPE_DB_BACKEND", "CORESCOPE_DATABASE_URL", "CORESCOPE_READER_DATABASE_URL", "CORESCOPE_STATE_DIR", "CORESCOPE_DECRYPT_TEST_CHILD":
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "CORESCOPE_DECRYPT_TEST_CHILD=1")
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err = cmd.Run()
	return out.Bytes(), diagnostic.String(), err
}

func TestDecryptNativeCLI(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "meshcore.db")
	db := sqliteExportFixture(t, path, true)
	for _, args := range [][]string{
		{"--channel", "#example"},
		{"--channel", "#example", "--db", path},
	} {
		out, diagnostic, err := runDecryptCLI(t, base, args...)
		if err != nil || !json.Valid(out) || !strings.Contains(diagnostic, "Scanned 0 GRP_TXT packets") {
			t.Fatalf("native CLI export failed: %v; %s", err, diagnostic)
		}
	}
	db.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runDecryptCLI(t, base, "--channel", "#example"); err == nil {
		t.Fatal("default CLI accepted a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("default CLI created a missing database")
	}
}
