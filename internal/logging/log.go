package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func Open(path string) (*os.File, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func Logf(w io.Writer, format string, args ...any) {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	message := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s\n", timestamp, message)
}
