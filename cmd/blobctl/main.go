package main

import (
	"context"
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
	"github.com/darvell/blob/internal/manifest"
	"github.com/darvell/blob/internal/tarball"
)

const usage = `blobctl — deploy folders to The Blob

Usage:
  blob init [--name <n>] [--port <p>] [--domain <d>]   Create a blob.yaml in current directory
  blob login --endpoint <url> [--token <t>]            Save endpoint and token (or run interactive)
  blob deploy [--name <n>] [--port <p>] [--domain <d>] Deploy current folder
  blob list                                            List apps
  blob status <app>                                    Show one app
  blob logs <app> [-n 200]                             Tail recent logs
  blob destroy <app>                                   Tear down an app
  blob whoami                                          Test connection
  blob version                                         Print version

Environment:
  BLOB_HOST     base URL of blobd, e.g. https://blob.irrigate.cc
  BLOB_TOKEN    bearer token for blobd

Manifest (blob.yaml) is optional. When present its values are defaults; flags override.
`

var version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
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
	case "destroy", "rm":
		cmdDestroy(args)
	case "whoami":
		cmdWhoami()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
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

type kv struct {
	flag, value string
}

func parseFlags(args []string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(args) {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			out["_"+strconv.Itoa(i)] = a
			i++
			continue
		}
		key := strings.TrimPrefix(a, "--")
		if eq := strings.Index(key, "="); eq >= 0 {
			out[key[:eq]] = key[eq+1:]
			i++
			continue
		}
		// boolean / value-next flag
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

func positional(flags map[string]string, idx int) string {
	return flags["_"+strconv.Itoa(idx)]
}

func cmdInit(args []string) {
	flags := parseFlags(args)
	name := flags["name"]
	if name == "" {
		cwd, _ := os.Getwd()
		name = strings.ToLower(filepath.Base(cwd))
	}
	m := &manifest.Manifest{
		Name:   name,
		Form:   "web-service",
		Port:   atoi(flags["port"]),
		Domain: flags["domain"],
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
		die("--endpoint is required, e.g. https://blob.irrigate.cc")
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

func atoi(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
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

func cmdDeploy(args []string) {
	flags := parseFlags(args)
	m := loadManifestForDeploy()
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
	c := mustClient()

	// If user specified --image we go through deploy-image (no upload).
	if m.Image != "" {
		req := &api.DeployRequest{
			App:    m.Name,
			Domain: m.Domain,
			Port:   m.Port,
			CPU:    m.CPU,
			Memory: m.Memory,
			Form:   m.Form,
			Env:    m.Env,
		}
		req.Tag = m.Image
		fmt.Printf("deploying image %s as %s...\n", m.Image, m.Name)
		out, err := c.DeployImage(context.Background(), req)
		if err != nil {
			die("%v", err)
		}
		printDeploy(out)
		return
	}

	// Upload source as a tarball, server builds and deploys.
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
	if err := c.UploadSource(context.Background(), m.Name, pr); err != nil {
		die("upload: %v", err)
	}
	if err := <-errCh; err != nil {
		die("pack: %v", err)
	}
	fmt.Printf("uploaded source in %s\n", time.Since(t0).Round(10*time.Millisecond))

	fmt.Println("building and deploying on server (this may take a minute on cold images)...")
	req := &api.DeployRequest{
		App:    m.Name,
		Domain: m.Domain,
		Port:   m.Port,
		CPU:    m.CPU,
		Memory: m.Memory,
		Form:   m.Form,
		Env:    m.Env,
	}
	out, err := c.Deploy(context.Background(), req)
	if err != nil {
		die("%v", err)
	}
	printDeploy(out)
}

func printDeploy(out *api.DeployResponse) {
	if len(out.Phases) > 0 {
		fmt.Println()
		for _, p := range out.Phases {
			ok := "ok"
			if !p.OK {
				ok = "FAIL"
			}
			fmt.Printf("  %-12s %5dms  %s %s\n", p.Name, p.DurationMS, ok, p.Note)
		}
		fmt.Println()
	}
	fmt.Println(out.URL)
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
	fmt.Printf("%-30s %-40s %-12s %s\n", "APP", "URL", "STATUS", "FORM")
	for _, a := range out.Apps {
		fmt.Printf("%-30s %-40s %-12s %s\n", a.App, a.URL, a.Status, a.Form)
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
	fmt.Printf("url:      %s\n", out.URL)
	fmt.Printf("image:    %s\n", out.Image)
	fmt.Printf("status:   %s\n", out.Status)
	fmt.Printf("form:     %s\n", out.Form)
	fmt.Printf("replicas: %d\n", out.Replicas)
	if len(out.Allocations) > 0 {
		fmt.Println("allocations:")
		for _, a := range out.Allocations {
			fmt.Printf("  %s  %-12s %-10s %s\n", a.ID[:8], a.Status, a.Health, a.Node)
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

// Used in cases where the CLI shells out for diagnostic purposes.
var _ = exec.Command
