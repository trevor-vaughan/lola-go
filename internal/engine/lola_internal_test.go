package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trevor-vaughan/lola-go/internal/ledger"
)

func TestShouldRemoveLola(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(dir string) error
		want    bool
		wantErr bool
	}{
		{
			name: "empty dir",
			setup: func(dir string) error {
				return nil
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "dir with empty ledger",
			setup: func(dir string) error {
				l := ledger.New()
				return l.Save(filepath.Join(dir, "lock.json"))
			},
			want:    true,
			wantErr: false,
		},
		{
			name: "dir with ledger that has one module",
			setup: func(dir string) error {
				l := ledger.New()
				l.Modules["example"] = &ledger.Entry{Source: "../example"}
				return l.Save(filepath.Join(dir, "lock.json"))
			},
			want:    false,
			wantErr: false,
		},
		{
			name: "dir with corrupt lock.json",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "lock.json"), []byte("{invalid json"), 0o644)
			},
			want:    false,
			wantErr: true,
		},
		{
			name: "dir with project.yml",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, "project.yml"), []byte("{}"), 0o644)
			},
			want:    false,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup failed: %v", err)
			}
			got, err := shouldRemoveLola(dir)
			if (err != nil) != tt.wantErr {
				t.Errorf("shouldRemoveLola() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("shouldRemoveLola() = %v, want %v", got, tt.want)
			}
		})
	}
}
