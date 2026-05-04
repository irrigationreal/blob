package importers

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func Kubernetes(path string) (*Result, error) {
	rendered, name, err := kubernetesManifestBytes(path)
	if err != nil {
		return nil, err
	}
	return kubernetesObjectsRendered("kubernetes", name, rendered)
}

func kubernetesManifestBytes(path string) ([]byte, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !fi.IsDir() {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		return b, kubernetesImportName(path), nil
	}

	var files []string
	if err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if isYAMLFile(p) {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no Kubernetes YAML files found in %s", path)
	}
	sort.Strings(files)

	var buf bytes.Buffer
	for i, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, "", err
		}
		if i > 0 {
			buf.WriteString("\n---\n")
		}
		buf.Write(b)
	}
	return buf.Bytes(), kubernetesImportName(path), nil
}

func isYAMLFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func kubernetesImportName(path string) string {
	name := filepath.Base(path)
	for _, suffix := range []string{".yaml", ".yml"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}
