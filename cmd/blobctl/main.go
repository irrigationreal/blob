package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/darvell/blob/internal/api"
	"github.com/darvell/blob/internal/client"
	"github.com/darvell/blob/internal/config"
	"github.com/darvell/blob/internal/manifest"
	"github.com/darvell/blob/internal/tarball"
)

const usage = `blobctl — deploy folders to The Blob

Usage:
  blob init [--name N] [--port P] [--domain D]    Create a blob.yaml in current directory
  blob login --endpoint URL [--token T]           Save endpoint and token
  blob deploy [--name N] [--port P] [--env ENV]   Deploy current folder (single or app)
  blob list                                       List apps
  blob status <app>                               Show one app
  blob logs <app> [-n 200]                        Tail recent logs
  blob scale <app> <replicas>                     Scale a service
  blob destroy <app> [--yes]                      Tear down an app

  blob secrets list [--env ENV]                   List secrets (names + lengths only)
  blob secrets set <name> [--env ENV] [--from FILE | --value V]
  blob secrets unset <name> [--env ENV]

  blob doctor                                     Run platform self-check

  blob whoami                                     Test connection
  blob version                                    Print version

Environment:
  BLOB_HOST     base URL of blobd, e.g. https://blob.irrigate.cc
  BLOB_TOKEN    bearer token for blobd

Manifest (blob.yaml) is optional for single-component deploys; flags override.
For multi-component apps, blob.yaml is required (use the components: list).
`

var version = "0.2.0"

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
	case "destroy", "rm":
		cmdDestroy(args)
	case "secrets":
		cmdSecrets(args)
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
	name := flags["name"]
	if name == "" {
		cwd, _ := os.Getwd()
		name = strings.ToLower(filepath.Base(cwd))
	}
	m := &manifest.Manifest{
		Component: manifest.Component{
			Name:   name,
			Form:   "web-service",
			Port:   atoi(flags["port"]),
			Domain: flags["domain"],
		},
	}
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
	fmt.Println("wrote blob.yaml")
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
		Port:        c.Port,
		CPU:         c.CPU,
		Memory:      c.Memory,
		Replicas:    c.Replicas,
		Env:         c.Env,
		Form:        c.Form,
		Schedule:    c.Schedule,
		Tag:         c.Image,
		Command:     c.Command,
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
	m := loadManifestForDeploy()
	if v := flags["env"]; v != "" {
		m.Environment = v
	}
	if m.Environment == "" {
		m.Environment = "prod"
	}

	c := mustClient()

	if m.IsApp() {
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
	if v := flags["form"]; v != "" {
		m.Form = v
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
	fmt.Printf("%-30s %-12s %-10s %-3s %s\n", "APP", "FORM", "STATUS", "N", "URL")
	for _, a := range out.Apps {
		fmt.Printf("%-30s %-12s %-10s %-3d %s\n", a.App, a.Form, a.Status, a.Replicas, a.URL)
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
		die("usage: blob logs <app> [-n 200]")
	}
	lines := atoi(flags["n"])
	if lines == 0 {
		lines = 200
	}
	c := mustClient()
	out, err := c.Logs(context.Background(), app, lines)
	if err != nil {
		die("%v", err)
	}
	for _, l := range out.Lines {
		fmt.Println(l)
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
