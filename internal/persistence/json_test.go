package persistence

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCorruptionPreservedAndReset(t *testing.T) {
	for _, data := range [][]byte{make([]byte, 22219), []byte(`{"count":`), []byte(`{"count":"bad"}`), []byte(`null`), {}} {
		path := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		value, err := Load[struct{ Count int }](path)
		if err != nil || value.Count != 0 {
			t.Fatalf("load: %+v %v", value, err)
		}
		backups, _ := filepath.Glob(path + ".corrupt-*")
		if len(backups) != 1 {
			t.Fatalf("backups: %v", backups)
		}
		got, err := os.ReadFile(backups[0])
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("corrupt evidence changed")
		}
		if err := Save(path, []byte(`{"count":7}`)); err != nil {
			t.Fatal(err)
		}
		value, err = Load[struct{ Count int }](path)
		if err != nil || value.Count != 7 {
			t.Fatalf("reload: %+v %v", value, err)
		}
	}
}

func TestIOErrorIsNotReset(t *testing.T) {
	if _, err := Load[map[string]any](t.TempDir()); err == nil {
		t.Fatal("directory read must fail")
	}
}

func TestSaveReplacesAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.json")
	for _, data := range []string{`{"count":1}`, `{"count":2}`} {
		if err := Save(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatalf("read: %s %v", got, err)
		}
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("unexpected files: %v", files)
	}
}
