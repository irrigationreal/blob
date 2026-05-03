package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
)

// backupsDir returns the per-instance backup root: /srv/blob/backups/postgres/<name>/
func (s *Server) postgresBackupsDir(name string) string {
	return filepath.Join(s.cfg.StateDir, "backups", "postgres", name)
}

// runningPostgresAlloc returns the alloc ID of the running postgres task for <name>.
func (s *Server) runningPostgresAlloc(ctx context.Context, name string) (string, error) {
	body, err := s.nomadGET(ctx, "/v1/job/pg-"+name+"/allocations")
	if err != nil {
		return "", err
	}
	var allocs []struct{ ID, ClientStatus string }
	if err := json.Unmarshal(body, &allocs); err != nil {
		return "", err
	}
	for _, a := range allocs {
		if a.ClientStatus == "running" {
			return a.ID, nil
		}
	}
	return "", errors.New("no running postgres allocation")
}

// backupPostgres runs pg_dump inside the running postgres container, gzips
// the output, and writes it to /srv/blob/backups/postgres/<name>/<UTC-ISO>.sql.gz.
// If a backup-config exists and is enabled, it ALSO ships to the remote.
func (s *Server) backupPostgres(ctx context.Context, name string) (*api.PostgresBackup, error) {
	cfg, _ := s.loadBackupConfig(name)
	if cfg != nil && !cfg.Enabled {
		cfg = nil
	}
	return s.backupPostgresWithShipping(ctx, name, cfg)
}

// backupPostgresWithShipping does the dump + (optional) ship in one go.
// Used by both the on-demand handler and the scheduler.
func (s *Server) backupPostgresWithShipping(ctx context.Context, name string, cfg *api.PostgresBackupConfig) (*api.PostgresBackup, error) {
	m, err := s.loadPostgres(name)
	if err != nil {
		return nil, errors.New("no such postgres")
	}
	allocID, err := s.runningPostgresAlloc(ctx, name)
	if err != nil {
		return nil, err
	}
	dir := s.postgresBackupsDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	filename := stamp + ".sql.gz"
	full := filepath.Join(dir, filename)

	dumpCmd := exec.CommandContext(ctx,
		"nomad", "alloc", "exec",
		"-i=false", "-t=false",
		"-task", "pg",
		allocID,
		"pg_dump",
		"-U", m.User,
		"--clean",
		"--if-exists",
		"--create",
		"-d", m.Database,
	)
	gzCmd := exec.CommandContext(ctx, "gzip", "-c")

	pr, pw := iopipe()
	dumpCmd.Stdout = pw
	dumpCmd.Stderr = os.Stderr
	gzCmd.Stdin = pr

	out, err := os.Create(full)
	if err != nil {
		return nil, err
	}
	gzCmd.Stdout = out
	gzCmd.Stderr = os.Stderr

	if err := gzCmd.Start(); err != nil {
		out.Close()
		return nil, fmt.Errorf("gzip start: %w", err)
	}
	if err := dumpCmd.Run(); err != nil {
		_ = pw.Close()
		_ = gzCmd.Wait()
		out.Close()
		_ = os.Remove(full)
		return nil, fmt.Errorf("pg_dump: %w", err)
	}
	if err := pw.Close(); err != nil {
		_ = gzCmd.Wait()
		out.Close()
		return nil, err
	}
	if err := gzCmd.Wait(); err != nil {
		out.Close()
		_ = os.Remove(full)
		return nil, fmt.Errorf("gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(full)
		return nil, err
	}

	st, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	bk := &api.PostgresBackup{
		Name:      name,
		Path:      full,
		Filename:  filename,
		BytesSize: st.Size(),
		CreatedAt: st.ModTime(),
		Local:     true,
	}
	if hash, err := sha256File(full); err == nil {
		bk.SHA256 = hex.EncodeToString(hash)
	}

	// Ship if a config exists and is enabled.
	if cfg != nil && cfg.Enabled {
		remoteURL, sha, err := s.shipBackup(ctx, cfg, full)
		if err != nil {
			// Don't fail the whole operation — local backup succeeded. Surface
			// in logs and keep the local file.
			stdLog("backup ship failed for %s: %v", name, err)
		} else {
			bk.Remote = true
			bk.RemoteURL = remoteURL
			if bk.SHA256 == "" {
				bk.SHA256 = sha
			}
		}
	}
	return bk, nil
}

// listPostgresBackups returns existing backup files for the named instance,
// newest first. Merges local files and (when configured + enabled) remote
// files in the off-host destination so the operator sees one unified view.
func (s *Server) listPostgresBackups(name string) (*api.ListPostgresBackupsResponse, error) {
	if _, err := s.loadPostgres(name); err != nil {
		return nil, errors.New("no such postgres")
	}
	dir := s.postgresBackupsDir(name)
	byName := map[string]*api.PostgresBackup{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sql.gz") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			st, err := os.Stat(full)
			if err != nil {
				continue
			}
			byName[e.Name()] = &api.PostgresBackup{
				Name:      name,
				Path:      full,
				Filename:  e.Name(),
				BytesSize: st.Size(),
				CreatedAt: st.ModTime(),
				Local:     true,
			}
			if hash, err := sha256File(full); err == nil {
				byName[e.Name()].SHA256 = hex.EncodeToString(hash)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Merge remote.
	if cfg, err := s.loadBackupConfig(name); err == nil && cfg.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if remote, err := s.listRemoteBackups(ctx, cfg); err == nil {
			for fn, size := range remote {
				if existing, ok := byName[fn]; ok {
					existing.Remote = true
					existing.RemoteURL = fmt.Sprintf("s3://%s/%s%s", cfg.S3Bucket, cfg.S3Prefix, fn)
				} else {
					t, _ := parseBackupTime(fn)
					byName[fn] = &api.PostgresBackup{
						Name:      name,
						Filename:  fn,
						BytesSize: size,
						CreatedAt: t,
						Local:     false,
						Remote:    true,
						RemoteURL: fmt.Sprintf("s3://%s/%s%s", cfg.S3Bucket, cfg.S3Prefix, fn),
					}
				}
			}
		}
	}
	out := &api.ListPostgresBackupsResponse{}
	for _, b := range byName {
		out.Backups = append(out.Backups, *b)
	}
	sort.Slice(out.Backups, func(i, j int) bool {
		return out.Backups[i].CreatedAt.After(out.Backups[j].CreatedAt)
	})
	return out, nil
}

// restorePostgres pipes a gzipped pg_dump back into the running container's psql.
// The backup's `--clean --if-exists --create` directives drop and recreate the
// database, so the round-trip is exact.
//
// `from` controls where to fetch the backup:
//   - "" or "local": local file under /srv/blob/backups/postgres/<name>/
//   - "s3": pull from the configured remote (must be enabled). path is the filename.
//   - "s3://bucket/key": pull directly from a fully-qualified S3 URL,
//     using the instance's backup-config credentials/endpoint.
func (s *Server) restorePostgres(ctx context.Context, name, pathOrAlias, from string, force bool) error {
	m, err := s.loadPostgres(name)
	if err != nil {
		return errors.New("no such postgres")
	}
	resolved, cleanup, err := s.materializeBackup(ctx, name, pathOrAlias, from)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	if !force {
		if has, _ := s.postgresDatabaseHasTables(ctx, m); has {
			return errors.New("database is non-empty; pass --force to overwrite (or `blob postgres connect` and DROP first)")
		}
	}
	allocID, err := s.runningPostgresAlloc(ctx, name)
	if err != nil {
		return err
	}

	gzCmd := exec.CommandContext(ctx, "gunzip", "-c", resolved)
	psqlCmd := exec.CommandContext(ctx,
		"nomad", "alloc", "exec",
		"-i=true", "-t=false",
		"-task", "pg",
		allocID,
		"psql", "-U", m.User, "-d", "postgres", "-v", "ON_ERROR_STOP=1",
	)
	pr, pw := iopipe()
	gzCmd.Stdout = pw
	gzCmd.Stderr = os.Stderr
	psqlCmd.Stdin = pr
	psqlCmd.Stdout = os.Stdout
	psqlCmd.Stderr = os.Stderr

	if err := psqlCmd.Start(); err != nil {
		return fmt.Errorf("psql start: %w", err)
	}
	if err := gzCmd.Run(); err != nil {
		_ = pw.Close()
		_ = psqlCmd.Wait()
		return fmt.Errorf("gunzip: %w", err)
	}
	if err := pw.Close(); err != nil {
		_ = psqlCmd.Wait()
		return err
	}
	if err := psqlCmd.Wait(); err != nil {
		return fmt.Errorf("psql restore: %w", err)
	}
	return nil
}

// materializeBackup returns a path on local disk that the restore path can
// gunzip + pipe into psql. For local sources, this is just the existing file.
// For S3 sources, it downloads to a temp file under the instance's backups dir
// and returns a cleanup that removes the temp file.
func (s *Server) materializeBackup(ctx context.Context, instance, pathOrAlias, from string) (resolved string, cleanup func(), err error) {
	switch {
	case from == "" || from == "local":
		p, err := s.resolveBackupPath(instance, pathOrAlias)
		if err != nil {
			return "", nil, err
		}
		return p, nil, nil
	case from == "s3":
		cfg, err := s.loadBackupConfig(instance)
		if err != nil {
			return "", nil, errors.New("no backup-config for instance; cannot restore --from s3")
		}
		if !cfg.Enabled {
			return "", nil, errors.New("backup-config is disabled; cannot restore --from s3")
		}
		filename, err := s.resolveRemoteBackupName(ctx, cfg, pathOrAlias)
		if err != nil {
			return "", nil, err
		}
		key := cfg.S3Prefix + filename
		tmp := filepath.Join(s.postgresBackupsDir(instance), ".restore-"+filename)
		if err := os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
			return "", nil, err
		}
		if err := s.downloadRemoteBackup(ctx, cfg, key, tmp); err != nil {
			_ = os.Remove(tmp)
			return "", nil, fmt.Errorf("download %s: %w", key, err)
		}
		return tmp, func() { _ = os.Remove(tmp) }, nil
	case strings.HasPrefix(from, "s3://"):
		// Full URL: extract bucket and key, but reuse the instance's creds.
		rest := strings.TrimPrefix(from, "s3://")
		i := strings.IndexByte(rest, '/')
		if i <= 0 {
			return "", nil, errors.New("invalid s3 URL; expected s3://bucket/key")
		}
		cfg, err := s.loadBackupConfig(instance)
		if err != nil {
			return "", nil, errors.New("no backup-config for instance; cannot use s3:// URL without credentials")
		}
		// Override bucket/key for this one call.
		override := *cfg
		override.S3Bucket = rest[:i]
		key := rest[i+1:]
		filename := lastPathComponent(key)
		tmp := filepath.Join(s.postgresBackupsDir(instance), ".restore-"+filename)
		if err := os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
			return "", nil, err
		}
		if err := s.downloadRemoteBackup(ctx, &override, key, tmp); err != nil {
			_ = os.Remove(tmp)
			return "", nil, fmt.Errorf("download %s: %w", from, err)
		}
		return tmp, func() { _ = os.Remove(tmp) }, nil
	default:
		return "", nil, fmt.Errorf("unknown --from %q (expected local | s3 | s3://bucket/key)", from)
	}
}

func (s *Server) resolveRemoteBackupName(ctx context.Context, cfg *api.PostgresBackupConfig, pathOrAlias string) (string, error) {
	if pathOrAlias != "" && pathOrAlias != "latest" {
		return pathOrAlias, nil
	}
	remote, err := s.listRemoteBackups(ctx, cfg)
	if err != nil {
		return "", err
	}
	if len(remote) == 0 {
		return "", errors.New("no remote backups exist for this instance")
	}
	// Pick the one with the newest UTC-ISO timestamp in the filename.
	var newest string
	var newestT time.Time
	for n := range remote {
		t, err := parseBackupTime(n)
		if err != nil {
			continue
		}
		if newest == "" || t.After(newestT) {
			newest = n
			newestT = t
		}
	}
	if newest == "" {
		return "", errors.New("no parseable remote backups")
	}
	return newest, nil
}

// resolveBackupPath turns a user-supplied path/filename/alias into an absolute
// path under the instance's backup directory. Empty or "latest" picks newest.
func (s *Server) resolveBackupPath(name, p string) (string, error) {
	if p == "" || p == "latest" {
		list, err := s.listPostgresBackups(name)
		if err != nil {
			return "", err
		}
		if len(list.Backups) == 0 {
			return "", errors.New("no backups exist for this instance")
		}
		return list.Backups[0].Path, nil
	}
	if filepath.IsAbs(p) {
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	}
	candidate := filepath.Join(s.postgresBackupsDir(name), p)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("backup not found: %s", p)
}

func (s *Server) postgresDatabaseHasTables(ctx context.Context, m *postgresMeta) (bool, error) {
	allocID, err := s.runningPostgresAlloc(ctx, m.Name)
	if err != nil {
		return false, err
	}
	out := s.output(ctx,
		"nomad", "alloc", "exec",
		"-i=false", "-t=false", "-task", "pg",
		allocID,
		"psql", "-U", m.User, "-d", m.Database, "-tAc",
		"select count(*) from pg_tables where schemaname='public'",
	)
	out = strings.TrimSpace(out)
	if out == "" || out == "0" {
		return false, nil
	}
	return true, nil
}

// HTTP handlers ---------------------------------------------------------------

// extends handlePostgresItem via routing — see Routes() registration.

func (s *Server) handlePostgresBackup(w http.ResponseWriter, r *http.Request, name string) {
	out, err := s.backupPostgres(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handlePostgresBackupsList(w http.ResponseWriter, r *http.Request, name string) {
	out, err := s.listPostgresBackups(name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handlePostgresRestore(w http.ResponseWriter, r *http.Request, name string) {
	var req api.RestorePostgresRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.restorePostgres(r.Context(), name, req.Path, req.From, req.Force); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"name": name, "restored": true})
}

// iopipe is a small wrapper around os.Pipe to keep the local file short.
func iopipe() (*os.File, *os.File) {
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	return r, w
}
