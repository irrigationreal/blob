// Package importers translates third-party app definitions into blob.yaml.
//
// Each importer focuses on the 80% case: a single web-facing service with
// optional sidecars, env, persistent volumes, and (for Procfile/fly) a
// declared command. Untranslatable bits — service meshes, complex healthchecks,
// release tasks, multi-stage deploys — are reported via Warnings on the
// returned Result, never silently dropped. The CLI prints those warnings
// before asking the operator to confirm the write.
package importers

import (
	"github.com/darvell/blob/internal/manifest"
)

// Result is what every importer returns: the converted manifest, the
// canonical YAML the caller should write, and a list of human-readable
// warnings about untranslated input.
type Result struct {
	Manifest *manifest.Manifest
	YAML     []byte
	// Warnings are surfaced to the user before write. Empty list means
	// the importer translated everything it understood without dropping
	// anything significant.
	Warnings []string
	// Source describes which importer ran, used in CLI output.
	Source string
	// ExtraFiles are additional artefacts (Dockerfile, .dockerignore, etc.)
	// the importer wants written next to blob.yaml. Map key is the path
	// relative to the import target dir.
	ExtraFiles map[string][]byte
}

// Render fills in Result.YAML from Result.Manifest. Importers usually call
// this last so they can mutate the manifest freely up to that point.
func (r *Result) Render() error {
	out, err := r.Manifest.Marshal()
	if err != nil {
		return err
	}
	r.YAML = out
	return nil
}
