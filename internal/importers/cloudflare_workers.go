package importers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/darvell/blob/internal/manifest"
)

const (
	cloudflareWorkerRoot        = ".blob-worker-root"
	cloudflareWorkerAdapterFile = ".blob-worker-adapter.mjs"
	cloudflareWorkerBundleFile  = ".blob-worker-worker.mjs"
)

const cloudflareWorkerAdapter = `import worker from "./` + cloudflareWorkerBundleFile + `";

function requestURL(event) {
  const query = new URLSearchParams(event.query || {}).toString();
  const host = (event.headers && event.headers.host) || "blob-worker.local";
  return "https://" + host + event.path + (query ? "?" + query : "");
}

export default async function handler(event) {
  const method = event.method || "GET";
  const init = { method, headers: event.headers || {} };
  if (method !== "GET" && method !== "HEAD") {
    init.body = Buffer.from(event.rawBody || "", "base64");
  }
  const request = new Request(requestURL(event), init);
  const fetchHandler = typeof worker === "function" ? worker : worker && worker.fetch;
  if (typeof fetchHandler !== "function") {
    throw new Error("Cloudflare Worker must export a default function or default.fetch");
  }
  const ctx = {
    waitUntil(promise) { Promise.resolve(promise).catch(err => console.error(err)); },
    passThroughOnException() {},
  };
  return fetchHandler.call(worker, request, process.env, ctx);
}
`

func CloudflareWorkers(path string) (*Result, error) {
	dir, cfgPath, cfg, err := loadCloudflareWorkersConfig(path)
	if err != nil {
		return nil, err
	}
	main := strings.TrimSpace(cfg.Main)
	if main == "" {
		main = detectCloudflareWorkerMain(dir)
	}
	if main == "" {
		return nil, fmt.Errorf("cloudflare-workers: no Worker entrypoint found; set main in wrangler.toml or add src/index.js")
	}
	main, err = cleanCloudflareWorkerPath(main)
	if err != nil {
		return nil, fmt.Errorf("cloudflare-workers main: %w", err)
	}
	if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(main))); err != nil || st.IsDir() {
		return nil, fmt.Errorf("cloudflare-workers main %q does not exist", main)
	}

	name := sanitizeName(cfg.Name)
	if name == "" {
		name = sanitizeName(filepath.Base(dir))
	}
	if name == "" {
		name = "worker"
	}
	env := mergeEnv(cfg.Vars)
	c := manifest.Component{
		Name:    name,
		Form:    "function",
		Root:    cloudflareWorkerRoot,
		Runtime: "nodejs",
		Handler: cloudflareWorkerAdapterFile,
		Build:   cloudflareWorkerBuildCommand(main, cfg.Build.Command),
		Env:     env,
	}
	if len(c.Env) == 0 {
		c.Env = nil
	}
	res := &Result{
		Source:     "cloudflare-workers",
		Manifest:   &manifest.Manifest{Component: c},
		ExtraFiles: map[string][]byte{cloudflareWorkerAdapterFile: []byte(cloudflareWorkerAdapter)},
	}
	res.Warnings = append(res.Warnings, cloudflareWorkerWarnings(cfg, cfgPath)...)
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

type cloudflareWorkersConfig struct {
	Name               string           `toml:"name" json:"name"`
	Main               string           `toml:"main" json:"main"`
	CompatibilityDate  string           `toml:"compatibility_date" json:"compatibility_date"`
	CompatibilityFlags []string         `toml:"compatibility_flags" json:"compatibility_flags"`
	Vars               map[string]any   `toml:"vars" json:"vars"`
	Env                map[string]any   `toml:"env" json:"env"`
	Route              any              `toml:"route" json:"route"`
	Routes             []map[string]any `toml:"routes" json:"routes"`
	WorkersDev         any              `toml:"workers_dev" json:"workers_dev"`
	Build              struct {
		Command string `toml:"command" json:"command"`
	} `toml:"build" json:"build"`
	Triggers struct {
		Crons []string `toml:"crons" json:"crons"`
	} `toml:"triggers" json:"triggers"`
	Assets struct {
		Directory string `toml:"directory" json:"directory"`
	} `toml:"assets" json:"assets"`
	KVNamespaces            []map[string]any `toml:"kv_namespaces" json:"kv_namespaces"`
	D1Databases             []map[string]any `toml:"d1_databases" json:"d1_databases"`
	R2Buckets               []map[string]any `toml:"r2_buckets" json:"r2_buckets"`
	Services                []map[string]any `toml:"services" json:"services"`
	AnalyticsEngineDatasets []map[string]any `toml:"analytics_engine_datasets" json:"analytics_engine_datasets"`
	DurableObjects          struct {
		Bindings []map[string]any `toml:"bindings" json:"bindings"`
	} `toml:"durable_objects" json:"durable_objects"`
	Queues struct {
		Producers []map[string]any `toml:"producers" json:"producers"`
		Consumers []map[string]any `toml:"consumers" json:"consumers"`
	} `toml:"queues" json:"queues"`
}

func loadCloudflareWorkersConfig(path string) (string, string, cloudflareWorkersConfig, error) {
	var cfg cloudflareWorkersConfig
	st, err := os.Stat(path)
	if err != nil {
		return "", "", cfg, err
	}
	if !st.IsDir() {
		dir := filepath.Dir(path)
		base := filepath.Base(path)
		if isCloudflareWorkersConfigName(base) {
			if err := parseCloudflareWorkersConfig(path, &cfg); err != nil {
				return "", "", cfg, err
			}
			return dir, path, cfg, nil
		}
		cfg.Main = base
		return dir, "", cfg, nil
	}
	for _, name := range []string{"wrangler.toml", "wrangler.json", "wrangler.jsonc"} {
		cfgPath := filepath.Join(path, name)
		if _, err := os.Stat(cfgPath); err == nil {
			if err := parseCloudflareWorkersConfig(cfgPath, &cfg); err != nil {
				return "", "", cfg, err
			}
			return path, cfgPath, cfg, nil
		}
	}
	return path, "", cfg, nil
}

func parseCloudflareWorkersConfig(path string, cfg *cloudflareWorkersConfig) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		if _, err := toml.Decode(string(b), cfg); err != nil {
			return fmt.Errorf("parse wrangler.toml: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(b, cfg); err != nil {
			return fmt.Errorf("parse wrangler.json: %w", err)
		}
	case ".jsonc":
		if err := json.Unmarshal(stripJSONC(b), cfg); err != nil {
			return fmt.Errorf("parse wrangler.jsonc: %w", err)
		}
	default:
		return fmt.Errorf("unsupported Cloudflare Workers config %s", path)
	}
	return nil
}

func isCloudflareWorkersConfigName(name string) bool {
	switch strings.ToLower(name) {
	case "wrangler.toml", "wrangler.json", "wrangler.jsonc":
		return true
	default:
		return false
	}
}

func detectCloudflareWorkerMain(dir string) string {
	for _, candidate := range []string{"src/index.ts", "src/index.mjs", "src/index.js", "_worker.ts", "_worker.mjs", "_worker.js", "worker.ts", "worker.mjs", "worker.js"} {
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(candidate))); err == nil && !st.IsDir() {
			return candidate
		}
	}
	return ""
}

func cleanCloudflareWorkerPath(path string) (string, error) {
	path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if path == "" || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return "", fmt.Errorf("%q must stay inside the source tree", path)
	}
	return path, nil
}

func cloudflareWorkerBuildCommand(main, prebuild string) string {
	bundle := "npx --yes esbuild " + shellQuote(main) + " --bundle --format=esm --platform=browser --target=es2022 --outfile=/out/" + cloudflareWorkerBundleFile
	adapterCopy := "cp /src/" + cloudflareWorkerAdapterFile + " /out/" + cloudflareWorkerAdapterFile
	parts := []string{
		"mkdir -p /tmp/work",
		"cp -a /src/. /tmp/work/",
		"cd /tmp/work",
		nodeInstallCommand,
	}
	prebuild = strings.TrimSpace(prebuild)
	if prebuild != "" {
		parts = append(parts, prebuild)
	}
	parts = append(parts, bundle)
	workdirBuild := strings.Join(parts, " && ")
	inner := workdirBuild
	if prebuild == "" {
		needsInstall := "[ -f /src/package.json ] || [ -f /src/pnpm-lock.yaml ] || [ -f /src/yarn.lock ] || [ -f /src/bun.lock ] || [ -f /src/bun.lockb ]"
		inner = "if " + needsInstall + "; then " + workdirBuild + "; else cd /src && " + bundle + "; fi"
	}
	inner += " && " + adapterCopy
	return "rm -rf " + cloudflareWorkerRoot + " && mkdir -p " + cloudflareWorkerRoot + " && docker run --rm -v \"$PWD:/src:ro\" -v \"$PWD/" + cloudflareWorkerRoot + ":/out\" node:22-alpine sh -lc " + shellQuote(inner)
}

func cloudflareWorkerWarnings(cfg cloudflareWorkersConfig, cfgPath string) []string {
	var warnings []string
	if cfgPath == "" {
		warnings = append(warnings, "no wrangler config found - imported detected Worker entrypoint with default Node.js compatibility")
	}
	if cfg.CompatibilityDate != "" || len(cfg.CompatibilityFlags) > 0 {
		warnings = append(warnings, "compatibility_date / compatibility_flags are not emulated; verify runtime behavior under Blob's Node function adapter")
	}
	if len(cfg.Env) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d Wrangler env blocks dropped - import one environment at a time by copying its vars/bindings to the top level before import", len(cfg.Env)))
	}
	if cfg.Route != nil || len(cfg.Routes) > 0 || cfg.WorkersDev != nil {
		warnings = append(warnings, "Cloudflare routes/workers_dev dropped - Blob publishes the function at its app hostname; attach custom domains separately if needed")
	}
	if len(cfg.Triggers.Crons) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d cron triggers dropped - recreate them with `blob jobs schedule`", len(cfg.Triggers.Crons)))
	}
	if cfg.Assets.Directory != "" {
		warnings = append(warnings, fmt.Sprintf("assets directory %q dropped - import static assets separately with form: static", cfg.Assets.Directory))
	}
	bindings := 0
	bindings += len(cfg.KVNamespaces)
	bindings += len(cfg.D1Databases)
	bindings += len(cfg.R2Buckets)
	bindings += len(cfg.DurableObjects.Bindings)
	bindings += len(cfg.Queues.Producers)
	bindings += len(cfg.Queues.Consumers)
	bindings += len(cfg.Services)
	bindings += len(cfg.AnalyticsEngineDatasets)
	if bindings > 0 {
		warnings = append(warnings, fmt.Sprintf("%d Cloudflare bindings dropped - recreate with Blob managed services, env, or secrets", bindings))
	}
	return warnings
}

func stripJSONC(in []byte) []byte {
	return stripJSONTrailingCommas(stripJSONComments(in))
}

func stripJSONComments(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inString := false
	escaped := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(in) {
			switch in[i+1] {
			case '/':
				for i < len(in) && in[i] != '\n' {
					i++
				}
				if i < len(in) {
					out = append(out, in[i])
				}
				continue
			case '*':
				i += 2
				for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
					if in[i] == '\n' {
						out = append(out, '\n')
					}
					i++
				}
				i++
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func stripJSONTrailingCommas(in []byte) []byte {
	out := make([]byte, 0, len(in))
	inString := false
	escaped := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(in) && (in[j] == ' ' || in[j] == '\t' || in[j] == '\r' || in[j] == '\n') {
				j++
			}
			if j < len(in) && (in[j] == '}' || in[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
