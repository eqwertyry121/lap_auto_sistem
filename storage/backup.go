package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BackupResult struct {
	Path    string
	Created bool
}

func (s *Store) BackupDaily(ctx context.Context, backupDir string, now time.Time) (BackupResult, error) {
	if s == nil || s.db == nil {
		return BackupResult{}, fmt.Errorf("store is not open")
	}
	backupDir = strings.TrimSpace(backupDir)
	if backupDir == "" {
		return BackupResult{}, fmt.Errorf("backup dir is empty")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return BackupResult{}, fmt.Errorf("create backup dir: %w", err)
	}

	finalPath := filepath.Join(backupDir, backupFileName(s.path, now))
	if info, err := os.Stat(finalPath); err == nil {
		if info.Size() > 0 {
			return BackupResult{Path: finalPath}, nil
		}
		if err := os.Remove(finalPath); err != nil {
			return BackupResult{}, fmt.Errorf("remove empty backup: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return BackupResult{}, fmt.Errorf("stat backup: %w", err)
	}

	tmp, err := os.CreateTemp(backupDir, ".sqlite-backup-*.tmp")
	if err != nil {
		return BackupResult{}, fmt.Errorf("create backup temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return BackupResult{}, fmt.Errorf("close backup temp file: %w", err)
	}
	if err := os.Remove(tmpPath); err != nil {
		return BackupResult{}, fmt.Errorf("prepare backup temp file: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return BackupResult{}, fmt.Errorf("sqlite backup: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return BackupResult{}, fmt.Errorf("publish backup: %w", err)
	}
	return BackupResult{Path: finalPath, Created: true}, nil
}

func backupFileName(dbPath string, now time.Time) string {
	base := filepath.Base(dbPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "sqlite"
	}
	if ext == "" {
		ext = ".db"
	}
	return fmt.Sprintf("%s-%s%s", sanitizeBackupName(name), now.Format("20060102"), ext)
}

func sanitizeBackupName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "sqlite"
	}
	return b.String()
}
