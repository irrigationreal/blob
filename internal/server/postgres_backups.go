package server

import (
	"context"
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

// backupPostgres runs pg_dumpall inside the running postgres container,
// gzips the output, and writes it to /srv/blob/backups/postgres/<name>/<UTC-ISO>.sql.gz.
func (s *Server) backupPostgres(ctx context.Context, name string) (*api.PostgresBackup, error) {
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

	// Pipe: nomad alloc exec <id> pg_dump --clean --if-exists --create -d <db> | gzip > full
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

	// Wire stdout(dump) -> stdin(gz); gzCmd.stdout -> file
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
		return nil, fmt.Errorf("pg_dumpall: %w", err)
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
	return &api.PostgresBackup{
		Name:      name,
		Path:      full,
		Filename:  filename,
		BytesSize: st.Size(),
		CreatedAt: st.ModTime(),
	}, nil
}

// listPostgresBackups returns existing backup files for the named instance,
// newest first.
func (s *Server) listPostgresBackups(name string) (*api.ListPostgresBackupsResponse, error) {
	if _, err := s.loadPostgres(name); err != nil {
		return nil, errors.New("no such postgres")
	}
	dir := s.postgresBackupsDir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &api.ListPostgresBackupsResponse{}, nil
		}
		return nil, err
	}
	out := &api.ListPostgresBackupsResponse{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql.gz") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		out.Backups = append(out.Backups, api.PostgresBackup{
			Name:      name,
			Path:      full,
			Filename:  e.Name(),
			BytesSize: st.Size(),
			CreatedAt: st.ModTime(),
		})
	}
	sort.Slice(out.Backups, func(i, j int) bool {
		return out.Backups[i].CreatedAt.After(out.Backups[j].CreatedAt)
	})
	return out, nil
}

// restorePostgres pipes a gzipped pg_dumpall back into the running container's psql.
// The backup's `--clean --if-exists` directives drop and recreate objects; this
// is the round-trip semantics the docs promise.
func (s *Server) restorePostgres(ctx context.Context, name, pathOrAlias string, force bool) error {
	m, err := s.loadPostgres(name)
	if err != nil {
		return errors.New("no such postgres")
	}
	resolved, err := s.resolveBackupPath(name, pathOrAlias)
	if err != nil {
		return err
	}
	if !force {
		// Sanity: refuse if database already has tables. The "drop existing
		// then restore" flow is intentional but we want the operator to opt in.
		if has, _ := s.postgresDatabaseHasTables(ctx, m); has {
			return errors.New("database is non-empty; pass --force to overwrite (or `blob postgres connect` and DROP first)")
		}
	}
	allocID, err := s.runningPostgresAlloc(ctx, name)
	if err != nil {
		return err
	}

	gzCmd := exec.CommandContext(ctx, "gunzip", "-c", resolved)
	// Connect to the maintenance "postgres" database so the dump's
	// DROP DATABASE / CREATE DATABASE statements can run against the target.
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
	if err := s.restorePostgres(r.Context(), name, req.Path, req.Force); err != nil {
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
