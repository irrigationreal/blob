// Package tarball builds a deterministic .tar.gz of a project folder
// while honoring .gitignore-style excludes that match the existing
// irrigate rsync excludes.
package tarball

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var defaultExcludes = []string{
	".git",
	"node_modules",
	".next",
	"dist",
	"build",
	".venv",
	"__pycache__",
	".DS_Store",
	"target",
	".terraform",
}

// Pack streams a gzipped tar of root to w. Top-level dirs/files matching
// defaultExcludes are skipped.
func Pack(root string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	root = filepath.Clean(root)
	excl := map[string]struct{}{}
	for _, e := range defaultExcludes {
		excl[e] = struct{}{}
	}

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip excluded directories at any depth
		parts := strings.Split(rel, string(os.PathSeparator))
		for _, p := range parts {
			if _, skip := excl[p]; skip {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		// Resolve symlinks: store target in linkname
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			hdr.Linkname = target
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
}
