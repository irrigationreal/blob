package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
	"github.com/darvell/blob/internal/client"
	"github.com/darvell/blob/internal/config"
	"github.com/darvell/blob/internal/detect"
	"github.com/darvell/blob/internal/importers"
	"github.com/darvell/blob/internal/manifest"
	"github.com/darvell/blob/internal/tarball"
)

const usage = `blobctl — deploy folders to The Blob

Usage:
  blob init [--name N] [--port P] [--domain D] [--form F] [--root D]
                                                  Create a blob.yaml in current directory (auto-detects)
  blob import compose <path>                      Translate docker-compose.yaml → blob.yaml (writes alongside)
  blob import procfile <path>                     Translate Heroku Procfile → blob.yaml
  blob import fly <path>                          Translate fly.toml → blob.yaml
  blob login --endpoint URL [--token T]           Save endpoint and token
  blob deploy [--name N] [--port P] [--env ENV]   Deploy current folder
       [--cpu C] [--memory M] [--replicas N]
  blob deploy --isolation kata                    Run the workload with Kata microVM isolation
  blob deploy --static [--root DIR]               Force static-site form (else auto-detected
                                                  from index.html when no blob.yaml exists)
  blob deploy --from <kind> <path>                Import then deploy in one shot
  blob list                                       List apps
  blob status <app>                               Show one app
  blob logs <app> [-n 200] [--since 5m] [--grep P] [--follow]
                                                  Tail logs (Loki when registered, else nomad alloc tail)
  blob scale <app> <replicas>                     Scale a service
  blob restart <app>                              Restart all allocations
  blob releases <app>                             Show deploy history
  blob destroy <app> [--yes]                      Tear down an app
  blob open <app>                                 Open the app's URL in your browser

  blob exec <app> -- <cmd ...>                    Run a command inside the app

  blob domains attach <app> <host> [--mode MODE]  Attach an extra hostname; prints DNS for user-external

  blob secrets list [--env ENV]                   List secrets (names + lengths only)
  blob secrets set <name> [--env ENV] [--from FILE | --value V]
  blob secrets unset <name> [--env ENV]

  blob nodes list                                 List Nomad client nodes + reserved/available capacity
  blob nodes recommend --memory M --cpu C [--disk D]
                                                  Check whether the current fleet can place that shape
  blob nodes drain <id>                           Drain a node (move workloads off)
  blob nodes undrain <id>                         Stop draining
  blob nodes join                                 Print a one-liner to join a new server to the Blob

  blob volumes list                               List per-app Docker volumes

  blob doctor                                     Run platform self-check

  blob audit list [--limit N]                     Show authenticated mutating API actions
  blob audit show <id>

  blob identity tokens list
  blob identity tokens create <name>              Prints the service token secret once
  blob identity tokens revoke <id> [--yes]
  blob identity grants list [--token ID]
  blob identity grants add <id> <scope>
  blob identity grants revoke <id> <scope> [--yes]

  blob status-pages enable <app>                  Publish /status/<app> HTML + JSON
  blob status-pages list
  blob status-pages show <app>
  blob status-pages disable <app> [--yes]

  blob postgres list
  blob postgres create <name> [--version V] [--database D]
  blob postgres url <name>                        Print full DATABASE_URL (with password)
  blob postgres connect <name>                    Open a psql shell using the live DSN
  blob postgres backup <name>                     Snapshot to /srv/blob/backups/postgres/<name>/<UTC>.sql.gz
  blob postgres backups <name>                    List existing backups
  blob postgres restore <name> [path|latest] [--force]
  blob postgres destroy <name> [--yes]

  blob postgres project list <instance>
  blob postgres project create <instance> <project> [--timeout 30s]
  blob postgres project url <instance> <project>
  blob postgres project timeout <instance> <project> <duration>
  blob postgres project destroy <instance> <project> [--yes]

  blob valkey list
  blob valkey create <name> [--version V]
  blob valkey url <name>                          Print full REDIS_URL (with password)
  blob valkey destroy <name> [--yes]

  blob loki list
  blob loki create <name> [--version V]
  blob loki url <name>
  blob loki destroy <name> [--yes]

  blob grafana list
  blob grafana create <name> [--version V] [--loki I] [--tempo I] [--prometheus I]
  blob grafana url <name>                         Print URL + admin password
  blob grafana destroy <name> [--yes]

  blob promtail list
  blob promtail create <name> --loki <instance>   System job — one alloc per node
  blob promtail destroy <name> [--yes]

  blob nats list
  blob nats create <name> [--version V]
  blob nats url <name>
  blob nats destroy <name> [--yes]

  blob tempo list
  blob tempo create <name> [--version V]
  blob tempo url <name>
  blob tempo destroy <name> [--yes]

  blob prometheus list
  blob prometheus create <name> [--version V]
  blob prometheus url <name>
  blob prometheus destroy <name> [--yes]

  blob services list                              Roll-up of every managed-service kind

  blob preview create <app> --branch <name>       Ephemeral deploy at <app>-<branch>.<base>
  blob preview list <app>
  blob preview destroy <app> <branch>

  blob webhook github setup <app>                 Generate HMAC secret + paste-it URL for github webhooks
  blob webhook github get <app>
  blob webhook github remove <app>

  blob storage list
  blob storage create <name> [--bucket B] [--version V]
  blob storage url <name>                         Print endpoint + bucket + keys + console URL
  blob storage destroy <name> [--yes]

  blob mysql list
  blob mysql create <name> [--version V] [--database D]
  blob mysql url <name>                           Print full mysql:// DSN (with password)
  blob mysql destroy <name> [--yes]

  blob clickhouse list
  blob clickhouse create <name> [--version V] [--database D]
  blob clickhouse url <name>                      Print full clickhouse:// native DSN
  blob clickhouse destroy <name> [--yes]

  blob mongodb list
  blob mongodb create <name> [--version V] [--database D]
  blob mongodb url <name>                         Print full mongodb:// URI (with password)
  blob mongodb destroy <name> [--yes]

  blob scylladb list
  blob scylladb create <name> [--version V] [--keyspace K]
  blob scylladb url <name>                        Print cassandra:// pseudo-URI (with password)
  blob scylladb destroy <name> [--yes]

  blob certs add <app> <hostname>                 Bind a custom hostname to an app and request LE cert
  blob certs list
  blob certs verify <hostname>                    Probe the live edge for a Let's Encrypt cert
  blob certs remove <hostname> [--yes]

  blob jobs run [<app>] --image IMG -- CMD...     One-off batch job, optionally inheriting <app>'s services env
  blob jobs schedule <name> [<app>] --cron 'EXPR' --image IMG -- CMD...
                                                  Periodic batch job
  blob jobs list
  blob jobs status <id>
  blob jobs logs <id> [--fire N]                  N=1 first fire, N=2 second; 0/omitted = most recent
  blob jobs cancel <id> [--yes]

  blob autoscale list
  blob autoscale set <app> --min N --max M --metric cpu|memory|http_qps --target P
                                                  [--cooldown-up 60s] [--cooldown-down 180s]
  blob autoscale get <app>
  blob autoscale unset <app>

  blob whoami                                     Test connection
  blob version                                    Print version
`

var version = "0.31.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("blobctl", version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	case "init":
		cmdInit(args)
	case "import":
		cmdImport(args)
	case "from-nextjs":
		if len(args) < 1 {
			die("usage: blob from-nextjs <dir> [--yes]")
		}
		cmdImport(append([]string{"nextjs", args[0]}, args[1:]...))
	case "from-netlify":
		if len(args) < 1 {
			die("usage: blob from-netlify <dir-or-netlify.toml> [--yes]")
		}
		cmdImport(append([]string{"netlify", args[0]}, args[1:]...))
	case "login":
		cmdLogin(args)
	case "deploy":
		cmdDeploy(args)
	case "list", "ls":
		cmdList()
	case "status":
		cmdStatus(args)
	case "logs":
		cmdLogs(args)
	case "scale":
		cmdScale(args)
	case "restart":
		cmdRestart(args)
	case "releases":
		cmdReleases(args)
	case "destroy", "rm":
		cmdDestroy(args)
	case "open":
		cmdOpen(args)
	case "exec":
		cmdExec(args)
	case "domains":
		cmdDomains(args)
	case "nodes":
		cmdNodes(args)
	case "audit":
		cmdAudit(args)
	case "identity":
		cmdIdentity(args)
	case "status-pages", "statuspage":
		cmdStatusPages(args)
	case "volumes":
		cmdVolumes(args)
	case "secrets":
		cmdSecrets(args)
	case "postgres", "pg":
		cmdPostgres(args)
	case "valkey", "redis":
		cmdValkey(args)
	case "loki":
		cmdLoki(args)
	case "grafana":
		cmdGrafana(args)
	case "promtail":
		cmdPromtail(args)
	case "nats":
		cmdNATS(args)
	case "tempo":
		cmdTempo(args)
	case "prometheus":
		cmdPrometheus(args)
	case "autoscale":
		cmdAutoscale(args)
	case "services":
		cmdServices(args)
	case "preview":
		cmdPreview(args)
	case "webhook":
		cmdWebhook(args)
	case "storage":
		cmdStorage(args)
	case "mysql":
		cmdMySQL(args)
	case "clickhouse":
		cmdClickHouse(args)
	case "mongodb", "mongo":
		cmdMongo(args)
	case "scylladb", "scylla":
		cmdScylla(args)
	case "certs":
		cmdCerts(args)
	case "jobs":
		cmdJobs(args)
	case "doctor":
		cmdDoctor()
	case "whoami":
		cmdWhoami()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "blob: "+format+"\n", a...)
	os.Exit(1)
}

func mustClient() *client.Client {
	cfg, err := config.Load()
	if err != nil {
		die("config: %v", err)
	}
	if cfg.Endpoint == "" {
		die("no endpoint set. Run `blob login --endpoint https://blob.irrigate.cc` or set BLOB_HOST")
	}
	return client.New(cfg.Endpoint, cfg.Token)
}

func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	pos := 0
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			out["_"+strconv.Itoa(pos)] = a
			pos++
			i++
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if eq := strings.Index(key, "="); eq >= 0 {
			out[key[:eq]] = key[eq+1:]
			i++
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			out[key] = args[i+1]
			i += 2
			continue
		}
		out[key] = "true"
		i++
	}
	return out
}

func positional(flags map[string]string, idx int) string { return flags["_"+strconv.Itoa(idx)] }
func atoi(s string) int                                  { n, _ := strconv.Atoi(s); return n }

func cmdInit(args []string) {
	flags := parseFlags(args)
	cwd, _ := os.Getwd()
	c, why := detect.Detect(cwd)
	// Apply flag overrides
	if v := flags["name"]; v != "" {
		c.Name = v
	}
	if v := flags["form"]; v != "" {
		c.Form = v
	}
	if v := flags["port"]; v != "" {
		c.Port = atoi(v)
	}
	if v := flags["domain"]; v != "" {
		c.Domain = v
	}
	if v := flags["root"]; v != "" {
		c.Root = v
	}
	m := &manifest.Manifest{Component: *c}
	if err := m.Validate(); err != nil {
		die("%v", err)
	}
	b, err := m.Marshal()
	if err != nil {
		die("marshal: %v", err)
	}
	if _, err := os.Stat("blob.yaml"); err == nil {
		die("blob.yaml already exists; refusing to overwrite")
	}
	if err := os.WriteFile("blob.yaml", b, 0o644); err != nil {
		die("write blob.yaml: %v", err)
	}
	fmt.Printf("wrote blob.yaml — %s\n", why)
}

// cmdImport translates a third-party manifest (compose / procfile / fly)
// into blob.yaml. Mirrors `blob init`'s shape: writes blob.yaml in the
// CWD (or alongside --path), prints warnings about anything that
// couldn't be translated, and shows a diff vs any existing blob.yaml.
func cmdImport(args []string) {
	if len(args) < 2 {
		die("usage: blob import <compose|procfile|fly|nextjs|netlify> <path> [--out PATH] [--yes]")
	}
	kind, srcPath := args[0], args[1]
	flags := parseFlags(args[2:])

	var (
		res *importers.Result
		err error
	)
	switch kind {
	case "compose":
		res, err = importers.Compose(srcPath)
	case "procfile":
		res, err = importers.Procfile(srcPath)
	case "fly":
		res, err = importers.Fly(srcPath)
	case "nextjs":
		// nextjs takes a directory, not a file
		res, err = importers.NextJS(srcPath)
	case "netlify":
		res, err = importers.Netlify(srcPath)
	default:
		die("unknown import kind %q (expected: compose | procfile | fly | nextjs | netlify)", kind)
	}
	if err != nil {
		die("import %s: %v", kind, err)
	}

	out := flags["out"]
	if out == "" {
		// Default: alongside the source. For directory-based importers
		// (nextjs) the source IS the dir. For file-based importers, use
		// the file's parent.
		baseDir := srcPath
		if fi, statErr := os.Stat(srcPath); statErr == nil && !fi.IsDir() {
			baseDir = filepath.Dir(srcPath)
		}
		out = filepath.Join(baseDir, "blob.yaml")
	}

	fmt.Printf("imported via %s from %s\n", res.Source, srcPath)
	fmt.Println()
	fmt.Println("--- generated blob.yaml ---")
	fmt.Print(string(res.YAML))
	fmt.Println("---")
	if len(res.Warnings) > 0 {
		fmt.Printf("\nWarnings (%d):\n", len(res.Warnings))
		for _, w := range res.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}

	// If a blob.yaml already exists at out, show a diff hint.
	if existing, err := os.ReadFile(out); err == nil && string(existing) != string(res.YAML) {
		fmt.Printf("\nNOTE: %s already exists. Use --yes to overwrite.\n", out)
		if flags["yes"] != "true" {
			die("aborted (existing %s); pass --yes to overwrite", out)
		}
	}

	if err := os.WriteFile(out, res.YAML, 0o644); err != nil {
		die("write %s: %v", out, err)
	}
	fmt.Printf("\nwrote %s\n", out)
	// Importers like nextjs ship a Dockerfile + .dockerignore alongside
	// blob.yaml. Refuse to overwrite existing files unless --yes is set.
	for rel, data := range res.ExtraFiles {
		extraPath := filepath.Join(filepath.Dir(out), rel)
		if existing, err := os.ReadFile(extraPath); err == nil && string(existing) != string(data) {
			if flags["yes"] != "true" {
				fmt.Printf("NOTE: %s already exists; left alone (pass --yes to overwrite)\n", extraPath)
				continue
			}
		}
		if err := os.WriteFile(extraPath, data, 0o644); err != nil {
			die("write %s: %v", extraPath, err)
		}
		fmt.Printf("wrote %s\n", extraPath)
	}
	fmt.Printf("Next: cd %s && blob deploy\n", filepath.Dir(out))
}

func cmdLogin(args []string) {
	flags := parseFlags(args)
	endpoint := strings.TrimRight(flags["endpoint"], "/")
	if endpoint == "" {
		die("--endpoint is required")
	}
	token := flags["token"]
	cfg := &config.Config{Endpoint: endpoint, Token: token}
	if err := config.Save(cfg); err != nil {
		die("save: %v", err)
	}
	fmt.Println("saved endpoint", endpoint)
	c := client.New(endpoint, token)
	if w, err := c.WhoAmI(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "warning: whoami failed:", err)
	} else {
		fmt.Println("connected as", w.Name)
	}
}

func cmdWhoami() {
	c := mustClient()
	w, err := c.WhoAmI(context.Background())
	if err != nil {
		die("%v", err)
	}
	fmt.Println(w.Name)
}

func loadManifestForDeploy() *manifest.Manifest {
	if _, err := os.Stat("blob.yaml"); err != nil {
		return &manifest.Manifest{}
	}
	m, err := manifest.Load("blob.yaml")
	if err != nil {
		die("read blob.yaml: %v", err)
	}
	return m
}

func componentToReq(app string, c *manifest.Component, env string) *api.DeployRequest {
	r := &api.DeployRequest{
		App:         c.Name,
		Environment: env,
		Domain:      c.Domain,
		Domains:     c.Domains,
		Port:        c.Port,
		CPU:         c.CPU,
		Memory:      c.Memory,
		Replicas:    c.Replicas,
		Env:         c.Env,
		Form:        c.Form,
		Schedule:    c.Schedule,
		Tag:         c.Image,
		Command:     c.Command,
		Isolation:   c.Isolation,
		Services:    c.Services,
		Root:        c.Root,
		Build:       c.Build,
		Index:       c.Index,
		NotFound:    c.NotFound,
		SPA:         c.SPA,
	}
	if app == "" {
		r.App = c.Name
	}
	for _, sb := range c.Secrets {
		r.Secrets = append(r.Secrets, api.SecretBinding{Env: sb.Env, Name: sb.Name})
	}
	for _, v := range c.Volumes {
		r.Volumes = append(r.Volumes, api.VolumeMount{Name: v.Name, Path: v.Path})
	}
	for _, sc := range c.Sidecars {
		r.Sidecars = append(r.Sidecars, api.Sidecar{Name: sc.Name, Image: sc.Image, CPU: sc.CPU, Memory: sc.Memory, Env: sc.Env, Args: sc.Args})
	}
	return r
}

func cmdDeploy(args []string) {
	flags := parseFlags(args)
	// --from <kind>: import a third-party manifest into ./blob.yaml first,
	// then deploy from the directory containing the source file. The path
	// to the source file is the first positional arg after the kind.
	if kind := flags["from"]; kind != "" {
		path := positional(flags, 0)
		if path == "" {
			die("usage: blob deploy --from <compose|procfile|fly> <path>")
		}
		// Force overwrite — operator opted in by passing --from.
		cmdImport([]string{kind, path, "--yes"})
		// Switch to the source dir so deploy reads the freshly-written
		// blob.yaml and tarballs that folder.
		dir := filepath.Dir(path)
		if err := os.Chdir(dir); err != nil {
			die("chdir %s: %v", dir, err)
		}
		fmt.Println()
		fmt.Println("--- now deploying ---")
	}
	m := loadManifestForDeploy()
	if v := flags["env"]; v != "" {
		m.Environment = v
	}
	if m.Environment == "" {
		m.Environment = "prod"
	}

	c := mustClient()

	if m.IsApp() {
		if v := flags["isolation"]; v != "" {
			for i := range m.Components {
				m.Components[i].Isolation = v
			}
		}
		if err := m.Validate(); err != nil {
			die("%v", err)
		}
		// Multi-component App: upload source under the app name, then deploy each component.
		// Each component is registered as a separate Nomad job named "<app>-<component>".
		if m.Name == "" {
			die("app manifest needs a top-level name")
		}
		if err := uploadSource(c, m.Name); err != nil {
			die("%v", err)
		}
		req := &api.DeployAppRequest{App: m.Name, Environment: m.Environment}
		for i := range m.Components {
			cr := componentToReq(m.Name, &m.Components[i], m.Environment)
			cr.App = m.Name + "-" + m.Components[i].Name
			req.Components = append(req.Components, *cr)
		}
		fmt.Printf("deploying app %s (%d components, env=%s)...\n", m.Name, len(req.Components), m.Environment)
		out, err := c.DeployApp(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		for _, comp := range out.Components {
			fmt.Printf("\n%s:\n", comp.JobID)
			printPhases(comp.Phases)
			if comp.URL != "" {
				fmt.Println(comp.URL)
			}
		}
		return
	}

	// Single component path
	if v := flags["name"]; v != "" {
		m.Name = v
	}
	if m.Name == "" {
		cwd, _ := os.Getwd()
		m.Name = strings.ToLower(filepath.Base(cwd))
	}
	if v := flags["domain"]; v != "" {
		m.Domain = v
	}
	if v := flags["port"]; v != "" {
		m.Port = atoi(v)
	}
	if v := flags["cpu"]; v != "" {
		m.CPU = atoi(v)
	}
	if v := flags["memory"]; v != "" {
		m.Memory = atoi(v)
	}
	if v := flags["replicas"]; v != "" {
		m.Replicas = atoi(v)
	}
	if v := flags["form"]; v != "" {
		m.Form = v
	}
	if v := flags["isolation"]; v != "" {
		m.Isolation = v
	}
	// --static is a shorthand for `--form static --root .`. Useful for
	// `blob deploy --static .` in any folder with index.html. We also
	// auto-detect the static path when no blob.yaml is present and the
	// CWD has an index.html with no Dockerfile — same shape detect.Detect
	// uses for `blob init`, just folded into the deploy entry so the
	// user doesn't need a manifest.
	if flags["static"] == "true" {
		m.Form = "static"
		if m.Root == "" {
			m.Root = "."
		}
	} else if m.Form == "" {
		// auto-detect static when there's no blob.yaml AND no manifest-set form
		// AND the source has the static-site shape.
		if _, err := os.Stat("blob.yaml"); err != nil {
			if _, err := os.Stat("index.html"); err == nil {
				if _, err := os.Stat("Dockerfile"); err != nil {
					m.Form = "static"
					if m.Root == "" {
						m.Root = "."
					}
					fmt.Println("auto-detected static site (index.html present, no Dockerfile)")
				}
			}
		}
	}
	if m.Form == "" {
		m.Form = "web-service"
	}
	if v := flags["image"]; v != "" {
		m.Image = v
	}
	if err := m.Validate(); err != nil {
		die("%v", err)
	}

	req := componentToReq("", &m.Component, m.Environment)
	req.App = m.Name

	if m.Image != "" {
		fmt.Printf("deploying image %s as %s (env=%s)...\n", m.Image, m.Name, m.Environment)
		out, err := c.DeployImage(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		printDeploy(out)
		return
	}

	if err := uploadSource(c, m.Name); err != nil {
		die("%v", err)
	}
	fmt.Printf("building and deploying %s (env=%s, form=%s)...\n", m.Name, m.Environment, m.Form)
	out, err := c.Deploy(context.Background(), req)
	if err != nil {
		die("%v", err)
	}
	printDeploy(out)
}

func uploadSource(c *client.Client, app string) error {
	cwd, _ := os.Getwd()
	fmt.Printf("packaging %s...\n", cwd)
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := tarball.Pack(cwd, pw)
		_ = pw.CloseWithError(err)
		errCh <- err
	}()
	t0 := time.Now()
	if err := c.UploadSource(context.Background(), app, pr); err != nil {
		return fmt.Errorf("upload: %v", err)
	}
	if err := <-errCh; err != nil {
		return fmt.Errorf("pack: %v", err)
	}
	fmt.Printf("uploaded source in %s\n", time.Since(t0).Round(10*time.Millisecond))
	return nil
}

func printPhases(phases []api.Phase) {
	if len(phases) == 0 {
		return
	}
	for _, p := range phases {
		ok := "ok"
		if !p.OK {
			ok = "FAIL"
		}
		fmt.Printf("  %-14s %5dms  %s %s\n", p.Name, p.DurationMS, ok, p.Note)
	}
}

func printDeploy(out *api.DeployResponse) {
	if len(out.Phases) > 0 {
		fmt.Println()
		printPhases(out.Phases)
		fmt.Println()
	}
	if out.URL != "" {
		fmt.Println(out.URL)
	} else {
		fmt.Printf("%s deployed (form=non-web)\n", out.JobID)
	}
}

func cmdList() {
	c := mustClient()
	out, err := c.List(context.Background())
	if err != nil {
		die("%v", err)
	}
	if len(out.Apps) == 0 {
		fmt.Println("no apps deployed")
		return
	}
	fmt.Printf("%-30s %-12s %-10s %-5s %-3s %s\n", "APP", "FORM", "STATUS", "ISOL", "N", "URL")
	for _, a := range out.Apps {
		isolation := a.Isolation
		if isolation == "" {
			isolation = "docker"
		}
		fmt.Printf("%-30s %-12s %-10s %-5s %-3d %s\n", a.App, a.Form, a.Status, isolation, a.Replicas, a.URL)
	}
}

func cmdStatus(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob status <app>")
	}
	c := mustClient()
	out, err := c.Status(context.Background(), app)
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("app:      %s\n", out.App)
	if out.Environment != "" {
		fmt.Printf("env:      %s\n", out.Environment)
	}
	fmt.Printf("url:      %s\n", out.URL)
	fmt.Printf("status:   %s\n", out.Status)
	fmt.Printf("form:     %s\n", out.Form)
	if out.Isolation != "" {
		fmt.Printf("isolation: %s\n", out.Isolation)
	}
	fmt.Printf("replicas: %d\n", out.Replicas)
	if len(out.Allocations) > 0 {
		fmt.Println("allocations:")
		for _, a := range out.Allocations {
			fmt.Printf("  %s  %-12s %s\n", a.ID[:8], a.Status, a.Node)
		}
	}
}

func cmdLogs(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob logs <app> [-n 200] [--since 5m] [--grep PATTERN] [--follow]")
	}
	lines := atoi(flags["n"])
	if lines == 0 {
		lines = 200
	}
	since := flags["since"]
	grep := flags["grep"]
	follow := flags["follow"] == "true"
	c := mustClient()
	printOnce := func(seen map[string]bool) {
		out, err := c.LogsWithOptions(context.Background(), app, lines, since, grep)
		if err != nil {
			die("%v", err)
		}
		for _, l := range out.Lines {
			if seen != nil {
				if seen[l] {
					continue
				}
				seen[l] = true
			}
			fmt.Println(l)
		}
	}
	if !follow {
		printOnce(nil)
		return
	}
	// --follow: poll Loki every 2s with --since shrunk to "10s" after the
	// first pass so we don't re-print the historical window. Dedup on the
	// raw line text — simple and good enough for human tailing.
	seen := map[string]bool{}
	printOnce(seen)
	since = "10s"
	for {
		time.Sleep(2 * time.Second)
		printOnce(seen)
	}
}

func cmdScale(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	rep := atoi(positional(flags, 1))
	if app == "" || (rep == 0 && positional(flags, 1) != "0") {
		die("usage: blob scale <app> <replicas>")
	}
	c := mustClient()
	if err := c.Scale(context.Background(), app, rep); err != nil {
		die("%v", err)
	}
	fmt.Printf("scaled %s -> %d\n", app, rep)
}

func cmdDestroy(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob destroy <app>")
	}
	if flags["yes"] != "true" {
		fmt.Printf("destroy %s? (type the name to confirm) ", app)
		var line string
		fmt.Fscanln(os.Stdin, &line)
		if line != app {
			die("aborted")
		}
	}
	c := mustClient()
	if err := c.Destroy(context.Background(), app); err != nil {
		die("%v", err)
	}
	fmt.Println("destroyed", app)
}

func cmdSecrets(args []string) {
	if len(args) == 0 {
		die("usage: blob secrets <list|set|unset> [...]")
	}
	sub := args[0]
	rest := args[1:]
	flags := parseFlags(rest)
	env := flags["env"]
	c := mustClient()
	switch sub {
	case "list", "ls":
		out, err := c.ListSecrets(context.Background(), env)
		if err != nil {
			die("%v", err)
		}
		if len(out.Secrets) == 0 {
			fmt.Println("no secrets in environment", firstNonEmpty(env, "prod"))
			return
		}
		fmt.Printf("%-30s %-10s %-6s %s\n", "NAME", "ENV", "LEN", "UPDATED")
		for _, s := range out.Secrets {
			fmt.Printf("%-30s %-10s %-6d %s\n", s.Name, firstNonEmpty(s.Environment, "prod"), s.Length, s.UpdatedAt.Format(time.RFC3339))
		}
	case "set":
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob secrets set <name> [--env ENV] [--from FILE | --value V]")
		}
		var value string
		if v := flags["value"]; v != "" {
			value = v
		} else if from := flags["from"]; from != "" {
			b, err := os.ReadFile(from)
			if err != nil {
				die("read --from: %v", err)
			}
			value = strings.TrimRight(string(b), "\n")
		} else {
			fmt.Print("value: ")
			r := bufio.NewReader(os.Stdin)
			line, _ := r.ReadString('\n')
			value = strings.TrimRight(line, "\n")
		}
		if err := c.SetSecret(context.Background(), env, name, value); err != nil {
			die("%v", err)
		}
		fmt.Printf("set %s (env=%s, %d bytes)\n", name, firstNonEmpty(env, "prod"), len(value))
	case "unset", "rm":
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob secrets unset <name> [--env ENV]")
		}
		if err := c.DeleteSecret(context.Background(), env, name); err != nil {
			die("%v", err)
		}
		fmt.Printf("deleted %s (env=%s)\n", name, firstNonEmpty(env, "prod"))
	default:
		die("unknown secrets subcommand: %s", sub)
	}
}

func cmdDoctor() {
	c := mustClient()
	out, err := c.Doctor(context.Background())
	if err != nil {
		die("%v", err)
	}
	if len(out.Issues) == 0 {
		fmt.Printf("doctor: %d checks, no issues\n", out.Checked)
		return
	}
	fmt.Printf("doctor: %d checks, %d issues\n\n", out.Checked, len(out.Issues))
	for _, i := range out.Issues {
		header := fmt.Sprintf("[%s] %s", i.Severity, i.Title)
		if i.App != "" {
			header += " — " + i.App
		}
		fmt.Println(header)
		if i.Detail != "" {
			fmt.Println("  detail:    ", i.Detail)
		}
		if i.Remediate != "" {
			fmt.Println("  remediate: ", i.Remediate)
		}
		fmt.Println()
	}
	for _, i := range out.Issues {
		if i.Severity == "P1" {
			os.Exit(1)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func cmdRestart(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob restart <app>")
	}
	c := mustClient()
	if err := c.Restart(context.Background(), app); err != nil {
		die("%v", err)
	}
	fmt.Println("restarted", app)
}

func cmdReleases(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob releases <app>")
	}
	c := mustClient()
	out, err := c.Releases(context.Background(), app)
	if err != nil {
		die("%v", err)
	}
	if len(out.Releases) == 0 {
		fmt.Println("no releases recorded")
		return
	}
	fmt.Printf("%-4s %-50s %s\n", "REV", "IMAGE", "CREATED")
	for _, r := range out.Releases {
		img := r.Image
		if len(img) > 49 {
			img = "..." + img[len(img)-46:]
		}
		fmt.Printf("%-4d %-50s %s\n", r.Revision, img, r.CreatedAt.Format(time.RFC3339))
	}
}

func cmdOpen(args []string) {
	flags := parseFlags(args)
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob open <app>")
	}
	c := mustClient()
	out, err := c.Status(context.Background(), app)
	if err != nil {
		die("%v", err)
	}
	if out.URL == "" {
		die("%s has no public URL (form=%s)", app, out.Form)
	}
	fmt.Println("opening", out.URL)
	openURL(out.URL)
}

func cmdExec(args []string) {
	// Split args at "--": before is positional [app], after is the command.
	app := ""
	var cmd []string
	seenSep := false
	for _, a := range args {
		if a == "--" {
			seenSep = true
			continue
		}
		if !seenSep {
			if app == "" {
				app = a
			}
			continue
		}
		cmd = append(cmd, a)
	}
	if app == "" || len(cmd) == 0 {
		die("usage: blob exec <app> -- <cmd ...>")
	}
	c := mustClient()
	out, err := c.Exec(context.Background(), app, cmd)
	if err != nil {
		die("%v", err)
	}
	fmt.Print(out.Output)
	if out.ExitCode != 0 {
		os.Exit(out.ExitCode)
	}
}

func cmdDomains(args []string) {
	if len(args) == 0 {
		die("usage: blob domains attach <app> <host> [--mode MODE]")
	}
	switch args[0] {
	case "attach":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		host := positional(flags, 1)
		mode := flags["mode"]
		if app == "" || host == "" {
			die("usage: blob domains attach <app> <host> [--mode platform-base|user-external]")
		}
		c := mustClient()
		out, err := c.AttachDomain(context.Background(), app, host, mode)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("attached %s -> %s\n", out.Host, out.URL)
		fmt.Println("mode:    ", out.Mode)
		if len(out.DNSRecords) > 0 {
			fmt.Println("\nDNS records to create at your registrar:")
			for _, d := range out.DNSRecords {
				fmt.Printf("  %s  %s  %s  TTL=%d\n", d.Type, d.Name, d.Value, d.TTL)
			}
			fmt.Println("\nThe certificate will issue automatically once the A record resolves to the platform.")
		}
	default:
		die("unknown domains subcommand: %s", args[0])
	}
}

func cmdNodes(args []string) {
	if len(args) == 0 {
		die("usage: blob nodes <list|recommend|drain|undrain|join>")
	}
	c := mustClient()
	switch args[0] {
	case "recommend":
		flags := parseFlags(args[1:])
		cpu := atoi(flags["cpu"])
		memory := atoi(flags["memory"])
		disk := atoi(flags["disk"])
		if cpu <= 0 || memory <= 0 {
			die("usage: blob nodes recommend --memory <MiB> --cpu <shares> [--disk <MiB>]")
		}
		out, err := c.RecommendPlacement(context.Background(), cpu, memory, disk)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("request: cpu=%d memory=%dMiB disk=%dMiB\n", out.CPU, out.MemoryMB, out.DiskMB)
		if out.Fits {
			fmt.Printf("fits:    %s (%s)\n", out.Node.Name, out.Node.Address)
			fmt.Printf("detail:  %s\n", out.Detail)
			return
		}
		fmt.Println("fits:    no")
		if out.Detail != "" {
			fmt.Printf("detail:  %s\n", out.Detail)
		}
		if out.Remediate != "" {
			fmt.Printf("fix:     %s\n", out.Remediate)
		}
	case "list", "ls":
		out, err := c.ListNodes(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Nodes) == 0 {
			fmt.Println("no nodes")
			return
		}
		fmt.Printf("%-10s %-18s %-15s %-8s %-10s %-4s %-17s %-21s %-23s %s\n", "ID", "NAME", "ADDR", "STATUS", "ELIGIBLE", "DC", "CPU R/A/T", "MEM R/A/T", "DISK R/A/T", "ALLOC")
		for _, n := range out.Nodes {
			id := n.ID
			if len(id) > 8 {
				id = id[:8]
			}
			elig := n.Eligible
			if n.Drain {
				elig = "draining"
			}
			fmt.Printf("%-10s %-18s %-15s %-8s %-10s %-4s %-17s %-21s %-23s %d\n",
				id, n.Name, n.Address, n.Status, elig, n.Datacenter,
				formatUsage(n.Resources.CPU, ""), formatUsage(n.Resources.MemoryMB, "MiB"), formatUsage(n.Resources.DiskMB, "MiB"), n.ActiveAllocations)
		}
	case "drain":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob nodes drain <id>")
		}
		if err := c.DrainNode(context.Background(), id, true); err != nil {
			die("%v", err)
		}
		fmt.Println("draining", id)
	case "undrain":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob nodes undrain <id>")
		}
		if err := c.DrainNode(context.Background(), id, false); err != nil {
			die("%v", err)
		}
		fmt.Println("undrained", id)
	case "join":
		out, err := c.JoinScript(context.Background())
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("Run this on the new node (Debian/Ubuntu, root):\n\n")
		fmt.Println(out.JoinScript)
		fmt.Printf("# Server address: %s\n", out.Address)
	default:
		die("unknown nodes subcommand: %s", args[0])
	}
}

func formatUsage(u api.ResourceUsage, suffix string) string {
	if suffix != "" {
		return fmt.Sprintf("%d/%d/%d%s", u.Reserved, u.Available, u.Total, suffix)
	}
	return fmt.Sprintf("%d/%d/%d", u.Reserved, u.Available, u.Total)
}

func cmdVolumes(args []string) {
	if len(args) == 0 || args[0] != "list" && args[0] != "ls" {
		die("usage: blob volumes list")
	}
	c := mustClient()
	out, err := c.ListVolumes(context.Background())
	if err != nil {
		die("%v", err)
	}
	if len(out.Volumes) == 0 {
		fmt.Println("no per-app volumes")
		return
	}
	fmt.Printf("%-30s %-20s %s\n", "APP", "VOLUME", "DOCKER NAME")
	for _, v := range out.Volumes {
		fmt.Printf("%-30s %-20s %s\n", v.App, v.Name, v.HostName)
	}
}

func cmdAudit(args []string) {
	if len(args) == 0 {
		die("usage: blob audit <list|show> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		flags := parseFlags(args[1:])
		limit := atoi(flags["limit"])
		out, err := c.ListAudit(context.Background(), limit)
		if err != nil {
			die("%v", err)
		}
		if len(out.Events) == 0 {
			fmt.Println("no audit events")
			return
		}
		fmt.Printf("%-30s %-20s %-8s %-28s %-4s %s\n", "ID", "TIME", "METHOD", "ACTION", "CODE", "PATH")
		for _, e := range out.Events {
			fmt.Printf("%-30s %-20s %-8s %-28s %-4d %s\n", e.ID, e.CreatedAt.Format(time.RFC3339), e.Method, e.Action, e.StatusCode, e.Path)
		}
	case "show":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob audit show <id>")
		}
		event, err := c.GetAudit(context.Background(), id)
		if err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(event, "", "  ")
		fmt.Println(string(b))
	default:
		die("unknown audit subcommand: %s", args[0])
	}
}

func cmdIdentity(args []string) {
	if len(args) == 0 {
		die("usage: blob identity <tokens|grants> ...")
	}
	c := mustClient()
	switch args[0] {
	case "tokens", "token":
		cmdIdentityTokens(c, args[1:])
	case "grants", "grant":
		cmdIdentityGrants(c, args[1:])
	default:
		die("unknown identity subcommand: %s", args[0])
	}
}

func cmdIdentityTokens(c *client.Client, args []string) {
	if len(args) == 0 {
		die("usage: blob identity tokens <list|create|revoke> ...")
	}
	switch args[0] {
	case "list", "ls":
		out, err := c.ListIdentityTokens(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Tokens) == 0 {
			fmt.Println("no service tokens")
			return
		}
		fmt.Printf("%-18s %-24s %-20s %-20s %s\n", "ID", "NAME", "CREATED", "REVOKED", "SCOPES")
		for _, t := range out.Tokens {
			revoked := "-"
			if !t.RevokedAt.IsZero() {
				revoked = t.RevokedAt.Format(time.RFC3339)
			}
			scopes := "-"
			if len(t.Scopes) > 0 {
				scopes = strings.Join(t.Scopes, ",")
			}
			fmt.Printf("%-18s %-24s %-20s %-20s %s\n", t.ID, t.Name, t.CreatedAt.Format(time.RFC3339), revoked, scopes)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob identity tokens create <name>")
		}
		out, err := c.CreateIdentityToken(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("created service token %s (%s)\n", out.Token.ID, out.Token.Name)
		fmt.Printf("secret: %s\n", out.Secret)
		fmt.Println("save this now; it is only shown once")
	case "revoke", "rm", "delete":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob identity tokens revoke <id> [--yes]")
		}
		if flags["yes"] != "true" {
			fmt.Printf("revoke service token %q? type the token id to confirm: ", id)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != id {
				die("aborted")
			}
		}
		if err := c.RevokeIdentityToken(context.Background(), id); err != nil {
			die("%v", err)
		}
		fmt.Printf("revoked service token %s\n", id)
	default:
		die("unknown identity tokens subcommand: %s", args[0])
	}
}

func cmdIdentityGrants(c *client.Client, args []string) {
	if len(args) == 0 {
		die("usage: blob identity grants <list|add|revoke> ...")
	}
	switch args[0] {
	case "list", "ls":
		flags := parseFlags(args[1:])
		out, err := c.ListIdentityGrants(context.Background(), flags["token"])
		if err != nil {
			die("%v", err)
		}
		if len(out.Grants) == 0 {
			fmt.Println("no grants")
			return
		}
		fmt.Printf("%-18s %-20s %s\n", "TOKEN", "SCOPE", "UPDATED")
		for _, g := range out.Grants {
			fmt.Printf("%-18s %-20s %s\n", g.TokenID, g.Scope, g.CreatedAt.Format(time.RFC3339))
		}
	case "add":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		scope := positional(flags, 1)
		if id == "" || scope == "" {
			die("usage: blob identity grants add <token-id> <scope>")
		}
		grant, err := c.AddIdentityGrant(context.Background(), id, scope)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("granted %s to %s\n", grant.Scope, grant.TokenID)
	case "revoke", "rm", "delete":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		scope := positional(flags, 1)
		if id == "" || scope == "" {
			die("usage: blob identity grants revoke <token-id> <scope> [--yes]")
		}
		if flags["yes"] != "true" {
			fmt.Printf("revoke grant %q from %q? type the scope to confirm: ", scope, id)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != scope {
				die("aborted")
			}
		}
		if err := c.RemoveIdentityGrant(context.Background(), id, scope); err != nil {
			die("%v", err)
		}
		fmt.Printf("revoked %s from %s\n", scope, id)
	default:
		die("unknown identity grants subcommand: %s", args[0])
	}
}

func cmdStatusPages(args []string) {
	if len(args) == 0 {
		die("usage: blob status-pages <enable|list|show|disable> ...")
	}
	c := mustClient()
	switch args[0] {
	case "enable":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob status-pages enable <app>")
		}
		out, err := c.EnableStatusPage(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("enabled status page for %s\n", out.Binding.App)
		fmt.Printf("url:     %s\n", out.Binding.URL)
		fmt.Printf("overall: %s\n", out.Status.Overall)
	case "list", "ls":
		out, err := c.ListStatusPages(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Pages) == 0 {
			fmt.Println("no status pages")
			return
		}
		fmt.Printf("%-30s %-60s %s\n", "APP", "URL", "CREATED")
		for _, p := range out.Pages {
			fmt.Printf("%-30s %-60s %s\n", p.App, p.URL, p.CreatedAt.Format(time.RFC3339))
		}
	case "show":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob status-pages show <app>")
		}
		out, err := c.ShowStatusPage(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		printStatusPage(out)
	case "disable", "rm":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob status-pages disable <app> [--yes]")
		}
		if flags["yes"] != "true" {
			fmt.Printf("disable status page for %q? type the app name to confirm: ", app)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != app {
				die("aborted")
			}
		}
		if err := c.DisableStatusPage(context.Background(), app); err != nil {
			die("%v", err)
		}
		fmt.Printf("disabled status page for %s\n", app)
	default:
		die("unknown status-pages subcommand: %s", args[0])
	}
}

func printStatusPage(out *api.StatusPageResponse) {
	fmt.Printf("app:      %s\n", out.Binding.App)
	fmt.Printf("url:      %s\n", out.Binding.URL)
	fmt.Printf("overall:  %s\n", out.Status.Overall)
	fmt.Printf("app:      %s (%d replicas)\n", out.Status.AppStatus.Status, out.Status.AppStatus.Replicas)
	fmt.Printf("route:    %s", out.Status.RouteHealth.Status)
	if out.Status.RouteHealth.StatusCode != 0 {
		fmt.Printf(" HTTP %d", out.Status.RouteHealth.StatusCode)
	}
	if out.Status.RouteHealth.LatencyMS != 0 {
		fmt.Printf(" %dms", out.Status.RouteHealth.LatencyMS)
	}
	if out.Status.RouteHealth.Error != "" {
		fmt.Printf(" - %s", out.Status.RouteHealth.Error)
	}
	fmt.Println()
	if len(out.Status.DoctorIssues) == 0 {
		fmt.Println("issues:   none")
		return
	}
	fmt.Println("issues:")
	for _, issue := range out.Status.DoctorIssues {
		fmt.Printf("  [%s] %s", issue.Severity, issue.Title)
		if issue.App != "" {
			fmt.Printf(" (%s)", issue.App)
		}
		fmt.Println()
		if issue.Detail != "" {
			fmt.Printf("       %s\n", issue.Detail)
		}
	}
}

// openURL opens a URL in the user's default browser.
func openURL(u string) {
	var cmd []string
	if _, err := exec.LookPath("open"); err == nil { // macOS
		cmd = []string{"open", u}
	} else if _, err := exec.LookPath("xdg-open"); err == nil { // most linux
		cmd = []string{"xdg-open", u}
	} else {
		fmt.Fprintln(os.Stderr, "open: no opener; the URL is:", u)
		return
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	_ = c.Start()
}

// --- managed services: postgres ---

func cmdPostgres(args []string) {
	if len(args) == 0 {
		die("usage: blob postgres <list|create|url|connect|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListPostgres(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Postgres) == 0 {
			fmt.Println("no postgres instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, p := range out.Postgres {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %s\n", p.Name, p.Version, p.Status, p.Host, p.Port, p.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres create <name> [--version V] [--database D]")
		}
		req := &api.CreatePostgresRequest{
			Name:     name,
			Version:  flags["version"],
			Database: flags["database"],
		}
		fmt.Printf("creating postgres %q (this provisions a Nomad job and waits for pg_isready)...\n", name)
		t0 := time.Now()
		out, err := c.CreatePostgres(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:      %s\n", out.Name)
		fmt.Printf("  version:   %s\n", out.Version)
		fmt.Printf("  host:      %s\n", out.Host)
		fmt.Printf("  port:      %d\n", out.Port)
		fmt.Printf("  database:  %s\n", out.Database)
		fmt.Printf("  user:      %s\n", out.User)
		fmt.Printf("  url:       %s\n", out.URLMasked)
		fmt.Println()
		fmt.Printf("To bind apps, add to blob.yaml:\n  services:\n    - %s\n", out.Name)
		fmt.Printf("Apps will receive DATABASE_URL, PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE.\n")
		fmt.Printf("Get the full DSN with: blob postgres url %s\n", out.Name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres url <name>")
		}
		url, err := c.PostgresURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(url)
	case "connect":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres connect <name>")
		}
		url, err := c.PostgresURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		if _, err := exec.LookPath("psql"); err != nil {
			fmt.Println(url)
			die("psql not found locally; the DSN is printed above")
		}
		cmd := exec.Command("psql", url)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy postgres %q? (data on disk is preserved as a Docker volume; type the name to confirm) ", name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyPostgres(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed postgres %q (Docker volume blob-pg-%s preserved)\n", name, name)
	case "backup":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres backup <name>")
		}
		fmt.Printf("snapshotting %s via pg_dump...\n", name)
		t0 := time.Now()
		out, err := c.BackupPostgres(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("backed up in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  path:  %s\n", out.Path)
		fmt.Printf("  size:  %s\n", humanBytes(out.BytesSize))
		fmt.Printf("  when:  %s\n", out.CreatedAt.Format(time.RFC3339))
	case "backups":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres backups <name>")
		}
		out, err := c.ListPostgresBackups(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		if len(out.Backups) == 0 {
			fmt.Printf("no backups for %s yet — try `blob postgres backup %s`\n", name, name)
			return
		}
		fmt.Printf("%-32s %-10s %-7s %-7s %-12s %s\n", "FILENAME", "SIZE", "LOCAL", "REMOTE", "SHA256", "CREATED")
		for _, b := range out.Backups {
			where := func(ok bool) string {
				if ok {
					return "yes"
				}
				return "no"
			}
			short := b.SHA256
			if len(short) > 12 {
				short = short[:12]
			}
			fmt.Printf("%-32s %-10s %-7s %-7s %-12s %s\n", b.Filename, humanBytes(b.BytesSize), where(b.Local), where(b.Remote), short, b.CreatedAt.Format(time.RFC3339))
		}
	case "restore":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob postgres restore <name> [path|latest] [--force] [--from local|s3|s3://bucket/key]")
		}
		path := positional(flags, 1)
		if path == "" {
			path = "latest"
		}
		force := flags["force"] == "true"
		from := flags["from"]
		fromLabel := ""
		if from != "" && from != "local" {
			fromLabel = " from " + from
		}
		fmt.Printf("restoring %s from %s%s%s...\n", name, path, fromLabel, ternary(force, " (force)", ""))
		t0 := time.Now()
		if err := c.RestorePostgresFrom(context.Background(), name, path, from, force); err != nil {
			die("%v", err)
		}
		fmt.Printf("restored in %s\n", time.Since(t0).Round(100*time.Millisecond))
	case "backup-config":
		cmdPostgresBackupConfig(args[1:])
		return
	case "project", "projects":
		cmdPostgresProject(args[1:])
		return
	default:
		die("unknown postgres subcommand: %s", args[0])
	}
}

// cmdPostgresProject handles `blob postgres project <create|list|url|destroy|timeout> ...`.
//
// Per-project users isolate two unrelated apps that share one Postgres
// instance: each project gets its own role + database + scoped password +
// statement_timeout, and apps bind via `services: [<instance>.<project>]`.
func cmdPostgresProject(args []string) {
	if len(args) == 0 {
		die("usage: blob postgres project <create|list|url|destroy|timeout> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		if instance == "" {
			die("usage: blob postgres project list <instance>")
		}
		out, err := c.ListPostgresProjects(context.Background(), instance)
		if err != nil {
			die("%v", err)
		}
		if len(out.Projects) == 0 {
			fmt.Printf("no projects on %s yet — try `blob postgres project create %s <project>`\n", instance, instance)
			return
		}
		fmt.Printf("%-20s %-15s %-15s %-12s %s\n", "PROJECT", "ROLE", "DATABASE", "TIMEOUT", "URL")
		for _, p := range out.Projects {
			fmt.Printf("%-20s %-15s %-15s %-12s %s\n",
				p.Project, p.Role, p.Database,
				humanDurationMS(p.StatementTimeoutMS),
				p.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		project := positional(flags, 1)
		if instance == "" || project == "" {
			die("usage: blob postgres project create <instance> <project> [--timeout DURATION]")
		}
		timeoutMS := 0
		if v := flags["timeout"]; v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				die("--timeout: %v", err)
			}
			timeoutMS = int(d / time.Millisecond)
		}
		fmt.Printf("creating project %q on %s...\n", project, instance)
		t0 := time.Now()
		out, err := c.CreatePostgresProject(context.Background(), instance, project, timeoutMS)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  instance:           %s\n", out.Instance)
		fmt.Printf("  project:            %s\n", out.Project)
		fmt.Printf("  role:               %s\n", out.Role)
		fmt.Printf("  database:           %s\n", out.Database)
		fmt.Printf("  statement_timeout:  %s\n", humanDurationMS(out.StatementTimeoutMS))
		fmt.Printf("  url:                %s\n", out.URLMasked)
		fmt.Println()
		fmt.Printf("To bind apps, add to blob.yaml:\n  services:\n    - %s.%s\n", out.Instance, out.Project)
	case "url":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		project := positional(flags, 1)
		if instance == "" || project == "" {
			die("usage: blob postgres project url <instance> <project>")
		}
		url, err := c.PostgresProjectURL(context.Background(), instance, project)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(url)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		project := positional(flags, 1)
		if instance == "" || project == "" {
			die("usage: blob postgres project destroy <instance> <project>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy project %q on %s? this drops the role AND database; type the project name to confirm: ", project, instance)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != project {
				die("aborted")
			}
		}
		if err := c.DestroyPostgresProject(context.Background(), instance, project); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed project %q on %s\n", project, instance)
	case "timeout":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		project := positional(flags, 1)
		duration := positional(flags, 2)
		if instance == "" || project == "" || duration == "" {
			die("usage: blob postgres project timeout <instance> <project> <duration>  (e.g. 2s, 60s, 5m)")
		}
		d, err := time.ParseDuration(duration)
		if err != nil {
			die("invalid duration: %v", err)
		}
		ms := int(d / time.Millisecond)
		out, err := c.SetPostgresProjectTimeout(context.Background(), instance, project, ms)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("set %s.%s statement_timeout = %s\n", out.Instance, out.Project, humanDurationMS(out.StatementTimeoutMS))
	default:
		die("unknown project subcommand: %s", args[0])
	}
}

func humanDurationMS(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	return d.String()
}

func ternary(b bool, a, c string) string {
	if b {
		return a
	}
	return c
}

func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/1024/1024)
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/1024/1024/1024)
	}
}

// --- managed services: valkey ---

func cmdValkey(args []string) {
	if len(args) == 0 {
		die("usage: blob valkey <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListValkey(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Valkey) == 0 {
			fmt.Println("no valkey instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, v := range out.Valkey {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %s\n", v.Name, v.Version, v.Status, v.Host, v.Port, v.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob valkey create <name> [--version V]")
		}
		req := &api.CreateValkeyRequest{Name: name, Version: flags["version"]}
		fmt.Printf("creating valkey %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateValkey(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:    %s\n", out.Name)
		fmt.Printf("  version: %s\n", out.Version)
		fmt.Printf("  host:    %s\n", out.Host)
		fmt.Printf("  port:    %d\n", out.Port)
		fmt.Printf("  url:     %s\n", out.URLMasked)
		fmt.Println()
		fmt.Printf("To bind apps, add to blob.yaml:\n  services:\n    - %s\n", out.Name)
		fmt.Printf("Apps will receive REDIS_URL, REDIS_HOST, REDIS_PORT, REDIS_PASSWORD.\n")
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob valkey url <name>")
		}
		url, err := c.ValkeyURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(url)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob valkey destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy valkey %q? (Docker volume blob-valkey-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyValkey(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed valkey %q (Docker volume blob-valkey-%s preserved)\n", name, name)
	default:
		die("unknown valkey subcommand: %s", args[0])
	}
}

// --- managed services: loki / grafana / promtail (v0.8) ---

func cmdLoki(args []string) {
	if len(args) == 0 {
		die("usage: blob loki <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListLoki(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Loki) == 0 {
			fmt.Println("no loki instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, l := range out.Loki {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %s\n", l.Name, l.Version, l.Status, l.Host, l.Port, l.URL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob loki create <name> [--version V]")
		}
		req := &api.CreateLokiRequest{Name: name, Version: flags["version"]}
		fmt.Printf("creating loki %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateLoki(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:    %s\n", out.Name)
		fmt.Printf("  version: %s\n", out.Version)
		fmt.Printf("  url:     %s\n", out.URL)
		fmt.Println()
		fmt.Printf("Bind apps with:\n  services:\n    - %s\n", out.Name)
		fmt.Printf("Apps will receive LOKI_URL, LOKI_PUSH_URL.\n")
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob loki url <name>")
		}
		l, err := c.GetLoki(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(l.URL)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob loki destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy loki %q? (Docker volume blob-loki-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyLoki(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed loki %q (Docker volume blob-loki-%s preserved)\n", name, name)
	default:
		die("unknown loki subcommand: %s", args[0])
	}
}

func cmdGrafana(args []string) {
	if len(args) == 0 {
		die("usage: blob grafana <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListGrafana(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Grafana) == 0 {
			fmt.Println("no grafana instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, g := range out.Grafana {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %s\n", g.Name, g.Version, g.Status, g.Host, g.Port, g.URL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob grafana create <name> [--version V] [--loki <instance>]")
		}
		req := &api.CreateGrafanaRequest{
			Name:               name,
			Version:            flags["version"],
			LokiInstance:       flags["loki"],
			TempoInstance:      flags["tempo"],
			PrometheusInstance: flags["prometheus"],
		}
		fmt.Printf("creating grafana %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateGrafana(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:     %s\n", out.Name)
		fmt.Printf("  version:  %s\n", out.Version)
		fmt.Printf("  url:      %s\n", out.URL)
		if out.LokiURL != "" {
			fmt.Printf("  loki ds:  %s\n", out.LokiURL)
		}
		fmt.Println()
		fmt.Printf("Run `blob grafana url %s` to fetch the admin password.\n", name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob grafana url <name>")
		}
		u, err := c.GrafanaURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("url:      %s\n", u.URL)
		fmt.Printf("user:     admin\n")
		fmt.Printf("password: %s\n", u.AdminPassword)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob grafana destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy grafana %q? (Docker volume blob-grafana-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyGrafana(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed grafana %q (Docker volume blob-grafana-%s preserved)\n", name, name)
	default:
		die("unknown grafana subcommand: %s", args[0])
	}
}

func cmdPromtail(args []string) {
	if len(args) == 0 {
		die("usage: blob promtail <list|create|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListPromtail(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Promtail) == 0 {
			fmt.Println("no promtail instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-20s %s\n", "NAME", "VERSION", "STATUS", "LOKI", "PUSH URL")
		for _, p := range out.Promtail {
			fmt.Printf("%-20s %-7s %-10s %-20s %s\n", p.Name, p.Version, p.Status, p.LokiInstance, p.LokiURL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" || flags["loki"] == "" {
			die("usage: blob promtail create <name> --loki <loki-instance> [--version V]")
		}
		req := &api.CreatePromtailRequest{
			Name:         name,
			Version:      flags["version"],
			LokiInstance: flags["loki"],
		}
		fmt.Printf("creating promtail %q (system job — one alloc per node)...\n", name)
		t0 := time.Now()
		out, err := c.CreatePromtail(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:    %s\n", out.Name)
		fmt.Printf("  version: %s\n", out.Version)
		fmt.Printf("  loki:    %s (%s)\n", out.LokiInstance, out.LokiURL)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob promtail destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy promtail %q? (type the name to confirm) ", name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyPromtail(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed promtail %q\n", name)
	default:
		die("unknown promtail subcommand: %s", args[0])
	}
}

// --- managed services: nats / tempo / prometheus (v0.10) ---

func cmdNATS(args []string) {
	if len(args) == 0 {
		die("usage: blob nats <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListNATS(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.NATS) == 0 {
			fmt.Println("no nats instances")
			return
		}
		fmt.Printf("%-20s %-12s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, n := range out.NATS {
			fmt.Printf("%-20s %-12s %-10s %-22s %-7d %s\n", n.Name, n.Version, n.Status, n.Host, n.Port, n.URL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob nats create <name> [--version V]")
		}
		fmt.Printf("creating nats %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateNATS(context.Background(), &api.CreateNATSRequest{Name: name, Version: flags["version"]})
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n  url: %s\n\nBind apps with:\n  services:\n    - %s\n", time.Since(t0).Round(100*time.Millisecond), out.URL, name)
		fmt.Println("Apps will receive NATS_URL.")
	case "url":
		name := positional(parseFlags(args[1:]), 0)
		if name == "" {
			die("usage: blob nats url <name>")
		}
		n, err := c.GetNATS(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(n.URL)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob nats destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy nats %q? (Docker volume blob-nats-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyNATS(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed nats %q\n", name)
	default:
		die("unknown nats subcommand: %s", args[0])
	}
}

func cmdTempo(args []string) {
	if len(args) == 0 {
		die("usage: blob tempo <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListTempo(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Tempo) == 0 {
			fmt.Println("no tempo instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-22s %s\n", "NAME", "VERSION", "STATUS", "HTTP", "OTLP", "URL")
		for _, t := range out.Tempo {
			fmt.Printf("%-20s %-7s %-10s %-22s %-22s %s\n",
				t.Name, t.Version, t.Status,
				fmt.Sprintf("%s:%d", t.Host, t.HTTPPort),
				t.OTLPGRPC, t.URL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob tempo create <name> [--version V]")
		}
		fmt.Printf("creating tempo %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateTempo(context.Background(), &api.CreateTempoRequest{Name: name, Version: flags["version"]})
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n  http:     %s\n  otlp:     %s\n\nBind apps with:\n  services:\n    - %s\n", time.Since(t0).Round(100*time.Millisecond), out.URL, out.OTLPGRPC, name)
		fmt.Println("Apps will receive TEMPO_URL, TEMPO_OTLP_GRPC, OTEL_EXPORTER_OTLP_ENDPOINT.")
	case "url":
		name := positional(parseFlags(args[1:]), 0)
		if name == "" {
			die("usage: blob tempo url <name>")
		}
		t, err := c.GetTempo(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("http: %s\notlp: %s\n", t.URL, t.OTLPGRPC)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob tempo destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy tempo %q? (Docker volume blob-tempo-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyTempo(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed tempo %q\n", name)
	default:
		die("unknown tempo subcommand: %s", args[0])
	}
}

func cmdPrometheus(args []string) {
	if len(args) == 0 {
		die("usage: blob prometheus <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListPrometheus(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Prometheus) == 0 {
			fmt.Println("no prometheus instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "URL")
		for _, p := range out.Prometheus {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %s\n", p.Name, p.Version, p.Status, p.Host, p.Port, p.URL)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob prometheus create <name> [--version V]")
		}
		fmt.Printf("creating prometheus %q...\n", name)
		t0 := time.Now()
		out, err := c.CreatePrometheus(context.Background(), &api.CreatePrometheusRequest{Name: name, Version: flags["version"]})
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n  url: %s\n\nBind apps with:\n  services:\n    - %s\n", time.Since(t0).Round(100*time.Millisecond), out.URL, name)
		fmt.Println("Apps will receive PROMETHEUS_URL.")
	case "url":
		name := positional(parseFlags(args[1:]), 0)
		if name == "" {
			die("usage: blob prometheus url <name>")
		}
		p, err := c.GetPrometheus(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(p.URL)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob prometheus destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy prometheus %q? (Docker volume blob-prometheus-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyPrometheus(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed prometheus %q\n", name)
	default:
		die("unknown prometheus subcommand: %s", args[0])
	}
}

// --- autoscale (v0.11) ---

func cmdAutoscale(args []string) {
	if len(args) == 0 {
		die("usage: blob autoscale <list|get|set|unset> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListAutoscale(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Autoscale) == 0 {
			fmt.Println("no autoscale configs")
			return
		}
		fmt.Printf("%-22s %-9s %-7s %-7s %-12s %-7s %s\n", "APP", "ENABLED", "MIN", "MAX", "METRIC", "TARGET", "COOLDOWN(up/down)")
		for _, c := range out.Autoscale {
			fmt.Printf("%-22s %-9t %-7d %-7d %-12s %-7.2f %s/%s\n",
				c.App, c.Enabled, c.Min, c.Max, c.Metric, c.Target,
				c.CooldownUp, c.CooldownDown)
		}
	case "get":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob autoscale get <app>")
		}
		cfg, err := c.GetAutoscale(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("app:           %s\nenabled:       %t\nmin:           %d\nmax:           %d\nmetric:        %s\ntarget:        %.2f\ncooldown_up:   %s\ncooldown_down: %s\n",
			cfg.App, cfg.Enabled, cfg.Min, cfg.Max, cfg.Metric, cfg.Target, cfg.CooldownUp, cfg.CooldownDown)
	case "set":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob autoscale set <app> --min N --max M --metric cpu|memory|http_qps --target P [--cooldown-up 60s] [--cooldown-down 180s]")
		}
		min := atoi(flags["min"])
		max := atoi(flags["max"])
		metric := flags["metric"]
		if metric == "" {
			metric = "cpu"
		}
		var target float64
		if t := flags["target"]; t != "" {
			t = strings.TrimSuffix(t, "%")
			fmt.Sscanf(t, "%f", &target)
		}
		cu := parseDurOrDefault(flags["cooldown-up"], 60*time.Second)
		cd := parseDurOrDefault(flags["cooldown-down"], 180*time.Second)
		if max == 0 {
			die("--max is required (>0)")
		}
		if target <= 0 {
			die("--target is required (>0)")
		}
		out, err := c.SetAutoscale(context.Background(), app, &api.AutoscaleConfig{
			App: app, Enabled: true,
			Min: min, Max: max,
			Metric: metric, Target: target,
			CooldownUp: cu, CooldownDown: cd,
		})
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("autoscale set: %s metric=%s target=%.2f range=%d..%d cooldown=%s/%s\n",
			out.App, out.Metric, out.Target, out.Min, out.Max, out.CooldownUp, out.CooldownDown)
	case "unset", "rm":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob autoscale unset <app>")
		}
		if err := c.UnsetAutoscale(context.Background(), app); err != nil {
			die("%v", err)
		}
		fmt.Printf("autoscale removed for %s\n", app)
	default:
		die("unknown autoscale subcommand: %s", args[0])
	}
}

func parseDurOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		die("invalid duration %q: %v", s, err)
	}
	return d
}

// --- services rollup (v0.11) ---

func cmdServices(args []string) {
	if len(args) > 0 && args[0] != "list" && args[0] != "ls" {
		die("usage: blob services list")
	}
	c := mustClient()
	out, err := c.ListServices(context.Background())
	if err != nil {
		die("%v", err)
	}
	if len(out.Services) == 0 {
		fmt.Println("no managed services registered")
		return
	}
	fmt.Printf("%-12s %-22s %-10s %-22s %-15s %s\n", "KIND", "NAME", "STATUS", "HOST", "PORTS", "URL")
	for _, s := range out.Services {
		ports := ""
		for i, p := range s.Ports {
			if i > 0 {
				ports += ","
			}
			ports += fmt.Sprintf("%d", p)
		}
		urlOut := ""
		if len(s.URLs) > 0 {
			urlOut = s.URLs[0]
		}
		fmt.Printf("%-12s %-22s %-10s %-22s %-15s %s\n", s.Kind, s.Name, s.Status, s.Host, ports, urlOut)
	}
}

// cmdPostgresBackupConfig handles `blob postgres backup-config <get|set|test|clear> <instance>`.
func cmdPostgresBackupConfig(args []string) {
	if len(args) == 0 {
		die("usage: blob postgres backup-config <get|set|test|clear> <instance>")
	}
	c := mustClient()
	switch args[0] {
	case "get":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		if instance == "" {
			die("usage: blob postgres backup-config get <instance>")
		}
		cfg, err := c.GetPostgresBackupConfig(context.Background(), instance)
		if err != nil {
			die("%v", err)
		}
		printBackupConfig(cfg)
	case "set":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		if instance == "" {
			die("usage: blob postgres backup-config set <instance> [flags]")
		}
		// Start from the existing config so partial updates work.
		cur, _ := c.GetPostgresBackupConfig(context.Background(), instance)
		cfg := &api.PostgresBackupConfig{Instance: instance}
		if cur != nil {
			cfg = cur
			cfg.Instance = instance
		}
		if v := flags["s3-endpoint"]; v != "" {
			cfg.S3Endpoint = v
		}
		if v := flags["s3-region"]; v != "" {
			cfg.S3Region = v
		}
		if v := flags["s3-bucket"]; v != "" {
			cfg.S3Bucket = v
		}
		if v, ok := flags["s3-prefix"]; ok {
			cfg.S3Prefix = v
		}
		if v := flags["s3-access-key-id"]; v != "" {
			cfg.S3AccessKeyID = v
		}
		if v := flags["s3-secret-access-key"]; v != "" {
			cfg.S3SecretAccessKey = v
		}
		if v := flags["s3-use-path-style"]; v == "true" {
			cfg.S3UsePathStyle = true
		}
		if v := flags["schedule"]; v != "" {
			cfg.Schedule = v
		}
		if v := flags["retention-daily"]; v != "" {
			cfg.RetentionDaily = atoi(v)
		}
		if v := flags["retention-weekly"]; v != "" {
			cfg.RetentionWeekly = atoi(v)
		}
		if v := flags["retention-monthly"]; v != "" {
			cfg.RetentionMonthly = atoi(v)
		}
		if flags["enable"] == "true" {
			cfg.Enabled = true
		}
		if flags["disable"] == "true" {
			cfg.Enabled = false
		}
		out, err := c.SetPostgresBackupConfig(context.Background(), cfg)
		if err != nil {
			die("%v", err)
		}
		fmt.Println("backup-config updated:")
		printBackupConfig(out)
	case "test":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		if instance == "" {
			die("usage: blob postgres backup-config test <instance>")
		}
		out, err := c.TestPostgresBackupConfig(context.Background(), instance)
		if err != nil {
			die("%v", err)
		}
		if out.OK {
			fmt.Println("ok:", out.Detail)
		} else {
			fmt.Fprintln(os.Stderr, "FAIL:", out.Detail)
			os.Exit(1)
		}
	case "clear":
		flags := parseFlags(args[1:])
		instance := positional(flags, 0)
		if instance == "" {
			die("usage: blob postgres backup-config clear <instance>")
		}
		if err := c.ClearPostgresBackupConfig(context.Background(), instance); err != nil {
			die("%v", err)
		}
		fmt.Printf("cleared backup-config for %s\n", instance)
	default:
		die("unknown backup-config subcommand: %s", args[0])
	}
}

func printBackupConfig(c *api.PostgresBackupConfig) {
	fmt.Printf("  instance:           %s\n", c.Instance)
	fmt.Printf("  enabled:            %t\n", c.Enabled)
	fmt.Printf("  destination_kind:   %s\n", c.DestinationKind)
	fmt.Printf("  s3_endpoint:        %s\n", c.S3Endpoint)
	fmt.Printf("  s3_region:          %s\n", c.S3Region)
	fmt.Printf("  s3_bucket:          %s\n", c.S3Bucket)
	fmt.Printf("  s3_prefix:          %s\n", c.S3Prefix)
	fmt.Printf("  s3_access_key_id:   %s\n", c.S3AccessKeyID)
	fmt.Printf("  s3_secret_access_key: %s\n", c.S3SecretAccessKey)
	fmt.Printf("  s3_use_path_style:  %t\n", c.S3UsePathStyle)
	fmt.Printf("  schedule:           %s (UTC)\n", c.Schedule)
	fmt.Printf("  retention:          daily=%d weekly=%d monthly=%d\n", c.RetentionDaily, c.RetentionWeekly, c.RetentionMonthly)
}

// --- preview environments (v0.12) ---

func cmdPreview(args []string) {
	if len(args) == 0 {
		die("usage: blob preview <create|list|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "create":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		branch := flags["branch"]
		if branch == "" {
			branch = positional(flags, 1)
		}
		if app == "" || branch == "" {
			die("usage: blob preview create <app> --branch <name>")
		}
		fmt.Printf("creating preview %s/%s ...\n", app, branch)
		t0 := time.Now()
		p, err := c.CreatePreview(context.Background(), app, branch)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n  url:    %s\n  job:    %s\n  domain: %s\n",
			time.Since(t0).Round(100*time.Millisecond), p.URL, p.JobID, p.Domain)
		for _, c := range p.Components {
			fmt.Printf("  └─ %s: %s\n", c.Name, c.URL)
		}
	case "list", "ls":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		if app == "" {
			die("usage: blob preview list <app>")
		}
		out, err := c.ListPreviews(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		if len(out.Previews) == 0 {
			fmt.Printf("no previews for %s\n", app)
			return
		}
		fmt.Printf("%-22s %-32s %-32s %s\n", "BRANCH", "JOB", "DOMAIN", "CREATED")
		for _, p := range out.Previews {
			fmt.Printf("%-22s %-32s %-32s %s\n", p.Branch, p.JobID, p.Domain, p.CreatedAt.Format(time.RFC3339))
			for _, c := range p.Components {
				fmt.Printf("  └─ %-19s %-32s %-32s\n", c.Name, c.JobID, c.Domain)
			}
		}
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		branch := positional(flags, 1)
		if branch == "" {
			branch = flags["branch"]
		}
		if app == "" || branch == "" {
			die("usage: blob preview destroy <app> <branch>")
		}
		if err := c.DestroyPreview(context.Background(), app, branch); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed preview %s/%s\n", app, branch)
	default:
		die("unknown preview subcommand: %s", args[0])
	}
}

// --- webhook receiver setup (v0.13) ---
//
// Today only `github` is supported. Provider goes second so we can
// add gitlab/bitbucket without breaking the URL shape.

func cmdWebhook(args []string) {
	if len(args) < 2 {
		die("usage: blob webhook <github> <setup|get|remove> <app>")
	}
	provider := args[0]
	if provider != "github" {
		die("unsupported provider %q (only 'github' for now)", provider)
	}
	sub := args[1]
	flags := parseFlags(args[2:])
	app := positional(flags, 0)
	if app == "" {
		die("usage: blob webhook github %s <app>", sub)
	}
	c := mustClient()
	switch sub {
	case "setup":
		out, err := c.SetupGitHubWebhook(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("github webhook configured for %s\n", out.App)
		fmt.Println()
		fmt.Println("Paste these into your repo's Settings → Webhooks → Add webhook:")
		fmt.Printf("  Payload URL:   %s\n", out.URL)
		fmt.Printf("  Content type:  application/json\n")
		fmt.Printf("  Secret:        %s\n", out.Secret)
		fmt.Printf("  SSL:           Enable SSL verification\n")
		fmt.Printf("  Events:        Send me everything (or just 'Pull requests')\n")
		fmt.Println()
		fmt.Println("On pull_request.opened/synchronize → blobd creates a preview at <app>-pr-<N>.<base>")
		fmt.Println("On pull_request.closed → blobd destroys it.")
	case "get":
		out, err := c.GetGitHubWebhook(context.Background(), app)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("app:    %s\nurl:    %s\nsecret: %s\n", out.App, out.URL, out.Secret)
	case "remove", "rm":
		if err := c.RemoveGitHubWebhook(context.Background(), app); err != nil {
			die("%v", err)
		}
		fmt.Printf("removed github webhook for %s\n", app)
	default:
		die("unknown webhook subcommand: %s", sub)
	}
}

// --- managed services: storage (v0.14) ---

func cmdStorage(args []string) {
	if len(args) == 0 {
		die("usage: blob storage <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListStorage(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Storage) == 0 {
			fmt.Println("no storage instances")
			return
		}
		fmt.Printf("%-20s %-10s %-22s %-8s %-12s %s\n", "NAME", "STATUS", "ENDPOINT", "API:UI", "BUCKET", "VERSION")
		for _, s := range out.Storage {
			fmt.Printf("%-20s %-10s %-22s %-8s %-12s %s\n",
				s.Name, s.Status, s.Endpoint,
				fmt.Sprintf("%d:%d", s.APIPort, s.UIPort),
				s.Bucket, s.Version)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob storage create <name> [--bucket B] [--version V]")
		}
		req := &api.CreateStorageRequest{
			Name:    name,
			Bucket:  flags["bucket"],
			Version: flags["version"],
		}
		fmt.Printf("creating storage %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateStorage(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:     %s\n", out.Name)
		fmt.Printf("  endpoint: %s\n", out.Endpoint)
		fmt.Printf("  bucket:   %s\n", out.Bucket)
		fmt.Printf("  console:  http://%s:%d\n", out.Host, out.UIPort)
		fmt.Println()
		fmt.Printf("To bind apps, add to blob.yaml:\n  services:\n    - %s\n", out.Name)
		fmt.Printf("Apps will receive S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY,\n")
		fmt.Printf("S3_REGION, S3_USE_PATH_STYLE plus the AWS_* aliases.\n")
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob storage url <name>")
		}
		u, err := c.StorageURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("endpoint:  %s\nbucket:    %s\naccess_key: %s\nsecret_key: %s\nconsole:   %s\n",
			u.Endpoint, u.Bucket, u.AccessKey, u.SecretKey, u.Console)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob storage destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy storage %q? (Docker volume blob-storage-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyStorage(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed storage %q (Docker volume blob-storage-%s preserved)\n", name, name)
	default:
		die("unknown storage subcommand: %s", args[0])
	}
}

// --- managed services: mysql / clickhouse (v0.17) ---

func cmdMySQL(args []string) {
	if len(args) == 0 {
		die("usage: blob mysql <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListMySQL(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.MySQL) == 0 {
			fmt.Println("no mysql instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %-12s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "DATABASE", "URL")
		for _, m := range out.MySQL {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %-12s %s\n", m.Name, m.Version, m.Status, m.Host, m.Port, m.Database, m.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mysql create <name> [--version V] [--database D]")
		}
		req := &api.CreateMySQLRequest{
			Name:     name,
			Version:  flags["version"],
			Database: flags["database"],
		}
		fmt.Printf("creating mysql %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateMySQL(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:     %s\n  version:  %s\n  host:     %s\n  port:     %d\n  database: %s\n  user:     %s\n  url:      %s\n",
			out.Name, out.Version, out.Host, out.Port, out.Database, out.User, out.URLMasked)
		fmt.Println()
		fmt.Printf("Bind apps with:\n  services:\n    - %s\nApps will receive MYSQL_URL, MYSQL_HOST, MYSQL_PORT, MYSQL_USER, MYSQL_PASSWORD, MYSQL_DATABASE.\n", name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mysql url <name>")
		}
		u, err := c.MySQLURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(u)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mysql destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy mysql %q? (Docker volume blob-mysql-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyMySQL(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed mysql %q\n", name)
	default:
		die("unknown mysql subcommand: %s", args[0])
	}
}

func cmdClickHouse(args []string) {
	if len(args) == 0 {
		die("usage: blob clickhouse <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListClickHouse(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.ClickHouse) == 0 {
			fmt.Println("no clickhouse instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-15s %-12s %s\n", "NAME", "VERSION", "STATUS", "HOST", "HTTP:NATIVE", "DATABASE", "URL")
		for _, m := range out.ClickHouse {
			fmt.Printf("%-20s %-7s %-10s %-22s %-15s %-12s %s\n",
				m.Name, m.Version, m.Status, m.Host,
				fmt.Sprintf("%d:%d", m.HTTPPort, m.NativePort),
				m.Database, m.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob clickhouse create <name> [--version V] [--database D]")
		}
		req := &api.CreateClickHouseRequest{
			Name:     name,
			Version:  flags["version"],
			Database: flags["database"],
		}
		fmt.Printf("creating clickhouse %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateClickHouse(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:        %s\n  version:     %s\n  host:        %s\n  http:        %s:%d\n  native:      %s:%d\n  database:    %s\n  user:        %s\n  url:         %s\n",
			out.Name, out.Version, out.Host, out.Host, out.HTTPPort, out.Host, out.NativePort,
			out.Database, out.User, out.URLMasked)
		fmt.Println()
		fmt.Printf("Bind apps with:\n  services:\n    - %s\nApps will receive CLICKHOUSE_URL, CLICKHOUSE_HTTP_URL, CLICKHOUSE_HOST, CLICKHOUSE_PORT,\nCLICKHOUSE_HTTP_PORT, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD, CLICKHOUSE_DATABASE.\n", name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob clickhouse url <name>")
		}
		u, err := c.ClickHouseURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(u)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob clickhouse destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy clickhouse %q? (Docker volume blob-clickhouse-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyClickHouse(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed clickhouse %q\n", name)
	default:
		die("unknown clickhouse subcommand: %s", args[0])
	}
}

func cmdMongo(args []string) {
	if len(args) == 0 {
		die("usage: blob mongodb <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListMongo(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Mongo) == 0 {
			fmt.Println("no mongodb instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %-15s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "DATABASE", "URL")
		for _, m := range out.Mongo {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %-15s %s\n",
				m.Name, m.Version, m.Status, m.Host, m.Port, m.Database, m.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mongodb create <name> [--version V] [--database D]")
		}
		req := &api.CreateMongoRequest{
			Name:     name,
			Version:  flags["version"],
			Database: flags["database"],
		}
		fmt.Printf("creating mongodb %q...\n", name)
		t0 := time.Now()
		out, err := c.CreateMongo(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:        %s\n  version:     %s\n  host:        %s\n  port:        %d\n  database:    %s\n  user:        %s\n  url:         %s\n",
			out.Name, out.Version, out.Host, out.Port, out.Database, out.User, out.URLMasked)
		fmt.Println()
		fmt.Printf("Bind apps with:\n  services:\n    - %s\nApps will receive MONGODB_URL, MONGO_URL, MONGODB_HOST, MONGODB_PORT,\nMONGODB_USER, MONGODB_PASSWORD, MONGODB_DATABASE.\n", name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mongodb url <name>")
		}
		u, err := c.MongoURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(u)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob mongodb destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy mongodb %q? (Docker volume blob-mongodb-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyMongo(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed mongodb %q\n", name)
	default:
		die("unknown mongodb subcommand: %s", args[0])
	}
}

func cmdScylla(args []string) {
	if len(args) == 0 {
		die("usage: blob scylladb <list|create|url|destroy> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListScylla(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Scylla) == 0 {
			fmt.Println("no scylladb instances")
			return
		}
		fmt.Printf("%-20s %-7s %-10s %-22s %-7s %-15s %s\n", "NAME", "VERSION", "STATUS", "HOST", "PORT", "KEYSPACE", "URL")
		for _, m := range out.Scylla {
			fmt.Printf("%-20s %-7s %-10s %-22s %-7d %-15s %s\n",
				m.Name, m.Version, m.Status, m.Host, m.Port, m.Keyspace, m.URLMasked)
		}
	case "create":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob scylladb create <name> [--version V] [--keyspace K]")
		}
		req := &api.CreateScyllaRequest{
			Name:     name,
			Version:  flags["version"],
			Keyspace: flags["keyspace"],
		}
		fmt.Printf("creating scylladb %q (this can take 90-180s — scylla bootstrap is slow)...\n", name)
		t0 := time.Now()
		out, err := c.CreateScylla(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("ready in %s\n", time.Since(t0).Round(100*time.Millisecond))
		fmt.Printf("  name:        %s\n  version:     %s\n  host:        %s\n  port:        %d\n  keyspace:    %s\n  user:        %s\n  url:         %s\n",
			out.Name, out.Version, out.Host, out.Port, out.Keyspace, out.User, out.URLMasked)
		fmt.Println()
		fmt.Printf("Bind apps with:\n  services:\n    - %s\nApps will receive SCYLLA_URL, SCYLLA_HOSTS, SCYLLA_PORT, SCYLLA_USER,\nSCYLLA_PASSWORD, SCYLLA_KEYSPACE plus CASSANDRA_* aliases.\n", name)
	case "url":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob scylladb url <name>")
		}
		u, err := c.ScyllaURL(context.Background(), name)
		if err != nil {
			die("%v", err)
		}
		fmt.Println(u)
	case "destroy", "rm":
		flags := parseFlags(args[1:])
		name := positional(flags, 0)
		if name == "" {
			die("usage: blob scylladb destroy <name>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("destroy scylladb %q? (Docker volume blob-scylladb-%s preserved; type the name to confirm) ", name, name)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != name {
				die("aborted")
			}
		}
		if err := c.DestroyScylla(context.Background(), name); err != nil {
			die("%v", err)
		}
		fmt.Printf("destroyed scylladb %q\n", name)
	default:
		die("unknown scylladb subcommand: %s", args[0])
	}
}

func cmdCerts(args []string) {
	if len(args) == 0 {
		die("usage: blob certs <add|list|verify|remove> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListCerts(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Certs) == 0 {
			fmt.Println("no custom-domain cert bindings")
			return
		}
		fmt.Printf("%-40s %-25s %-10s %-30s %s\n", "HOSTNAME", "APP", "VERIFIED", "ISSUER", "LAST PROBE")
		for _, b := range out.Certs {
			verified := "no"
			if b.Verified {
				verified = "yes"
			}
			lp := "-"
			if !b.LastProbe.IsZero() {
				lp = b.LastProbe.Format(time.RFC3339)
			}
			fmt.Printf("%-40s %-25s %-10s %-30s %s\n", b.Hostname, b.App, verified, b.LastIssuer, lp)
			if b.LastError != "" {
				fmt.Printf("    last error: %s\n", b.LastError)
			}
		}
	case "add":
		flags := parseFlags(args[1:])
		app := positional(flags, 0)
		host := positional(flags, 1)
		if app == "" || host == "" {
			die("usage: blob certs add <app> <hostname>")
		}
		out, err := c.AddCert(context.Background(), app, host)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("bound %s → %s\n", out.Binding.Hostname, out.Binding.App)
		if len(out.DNSRecords) > 0 {
			fmt.Println("DNS records to create:")
			for _, r := range out.DNSRecords {
				fmt.Printf("  %s %s %s (TTL %d)\n", r.Type, r.Name, r.Value, r.TTL)
			}
		}
		if out.Note != "" {
			fmt.Println(out.Note)
		}
	case "verify":
		flags := parseFlags(args[1:])
		host := positional(flags, 0)
		if host == "" {
			die("usage: blob certs verify <hostname>")
		}
		out, err := c.VerifyCert(context.Background(), host)
		if err != nil {
			die("%v", err)
		}
		b := out.Binding
		if b.Verified {
			fmt.Printf("verified %s — issuer %q\n", b.Hostname, b.LastIssuer)
		} else {
			fmt.Printf("NOT verified %s\n", b.Hostname)
			if b.LastError != "" {
				fmt.Printf("  %s\n", b.LastError)
			}
		}
	case "remove", "rm":
		flags := parseFlags(args[1:])
		host := positional(flags, 0)
		if host == "" {
			die("usage: blob certs remove <hostname>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("remove cert binding %q? (the LE cert in /srv/traefik/acme.json is preserved; type the hostname to confirm) ", host)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != host {
				die("aborted")
			}
		}
		if err := c.RemoveCert(context.Background(), host); err != nil {
			die("%v", err)
		}
		fmt.Printf("removed %q\n", host)
	default:
		die("unknown certs subcommand: %s", args[0])
	}
}

// splitOnDashDash returns (before, after) around the first standalone
// "--" sentinel. Used for `blob jobs run … -- CMD ARGS …` so user
// commands aren't tangled up with flag parsing.
func splitOnDashDash(args []string) ([]string, []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

func cmdJobs(args []string) {
	if len(args) == 0 {
		die("usage: blob jobs <run|schedule|list|status|logs|cancel> ...")
	}
	c := mustClient()
	switch args[0] {
	case "list", "ls":
		out, err := c.ListJobs(context.Background())
		if err != nil {
			die("%v", err)
		}
		if len(out.Jobs) == 0 {
			fmt.Println("no jobs")
			return
		}
		fmt.Printf("%-30s %-10s %-25s %-15s %-12s %-8s %s\n", "NAME", "KIND", "APP", "CRON", "STATUS", "FIRES", "IMAGE")
		for _, j := range out.Jobs {
			fires := ""
			if j.Kind == "cronjob" {
				fires = fmt.Sprintf("%d/%d", j.CompletedFires, j.FireCount)
			}
			fmt.Printf("%-30s %-10s %-25s %-15s %-12s %-8s %s\n",
				j.Name, j.Kind, j.App, j.Cron, j.Status, fires, j.Image)
		}
	case "run":
		head, cmd := splitOnDashDash(args[1:])
		flags := parseFlags(head)
		image := flags["image"]
		if image == "" {
			die("usage: blob jobs run [<app>] --image IMG [--name N] [--cpu C] [--memory M] [--timeout S] -- CMD ARGS...")
		}
		req := &api.RunJobRequest{
			App:     positional(flags, 0), // optional
			Image:   image,
			Name:    flags["name"],
			Command: cmd,
			CPU:     atoi(flags["cpu"]),
			Memory:  atoi(flags["memory"]),
			Timeout: atoi(flags["timeout"]),
		}
		fmt.Printf("running job (image=%s, app=%s)...\n", req.Image, req.App)
		out, err := c.RunJob(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("  id:        %s\n  name:      %s\n  status:    %s\n  exit:      %d\n",
			out.ID, out.Name, out.Status, out.ExitCode)
		if !out.FinishedAt.IsZero() {
			fmt.Printf("  finished:  %s\n", out.FinishedAt.Format(time.RFC3339))
		}
		fmt.Printf("\nFetch logs: blob jobs logs %s\n", out.ID)
	case "schedule":
		head, cmd := splitOnDashDash(args[1:])
		flags := parseFlags(head)
		name := positional(flags, 0)
		app := positional(flags, 1)
		image := flags["image"]
		cron := flags["cron"]
		if name == "" || image == "" || cron == "" {
			die("usage: blob jobs schedule <name> [<app>] --cron 'EXPR' --image IMG -- CMD ARGS...")
		}
		req := &api.ScheduleJobRequest{
			Name:    name,
			App:     app,
			Cron:    cron,
			Image:   image,
			Command: cmd,
			CPU:     atoi(flags["cpu"]),
			Memory:  atoi(flags["memory"]),
		}
		out, err := c.ScheduleJob(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("scheduled %s (cron=%s, app=%s)\n  id: %s\n  status: %s\n", out.Name, out.Cron, out.App, out.ID, out.Status)
	case "status":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob jobs status <id>")
		}
		out, err := c.StatusJob(context.Background(), id)
		if err != nil {
			die("%v", err)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	case "logs":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob jobs logs <id> [--fire N]")
		}
		fire := atoi(flags["fire"])
		out, err := c.JobLogs(context.Background(), id, fire)
		if err != nil {
			die("%v", err)
		}
		fmt.Printf("--- stdout (fire=%d) ---\n%s", out.Fire, out.Stdout)
		if strings.TrimSpace(out.Stderr) != "" {
			fmt.Printf("--- stderr ---\n%s", out.Stderr)
		}
	case "cancel", "rm":
		flags := parseFlags(args[1:])
		id := positional(flags, 0)
		if id == "" {
			die("usage: blob jobs cancel <id>")
		}
		if flags["yes"] != "true" {
			fmt.Printf("cancel job %q? (Nomad job will be stopped + purged; type the id to confirm) ", id)
			var line string
			fmt.Fscanln(os.Stdin, &line)
			if line != id {
				die("aborted")
			}
		}
		if err := c.CancelJob(context.Background(), id); err != nil {
			die("%v", err)
		}
		fmt.Printf("cancelled %q\n", id)
	default:
		die("unknown jobs subcommand: %s", args[0])
	}
}
