package importers

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/darvell/blob/internal/manifest"
)

// Helm renders a chart with `helm template` and translates common Kubernetes
// workload shapes into blob.yaml. The importer intentionally handles the
// portable 80%: Deployments, StatefulSets, Services, Ingresses, Jobs, and
// CronJobs. Kubernetes-specific scheduling, policy, RBAC, probes, and storage
// details are reported as warnings rather than silently dropped.
func Helm(path string) (*Result, error) {
	chartName := helmChartName(path)
	out, err := exec.Command("helm", "template", "blob-import", path).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("helm template: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("helm template: %w", err)
	}
	return helmRendered(chartName, out)
}

func helmRendered(chartName string, rendered []byte) (*Result, error) {
	res := &Result{Source: "helm"}
	objects, err := decodeHelmObjects(rendered)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("helm template produced no Kubernetes objects")
	}

	services := map[string]helmService{}
	serviceOrder := []string{}
	ingresses := []helmIngress{}
	workloads := []helmWorkload{}
	for _, obj := range objects {
		switch obj.Kind {
		case "Service":
			svc := helmServiceFromObject(obj, res)
			services[svc.Name] = svc
			serviceOrder = append(serviceOrder, svc.Name)
		case "Ingress":
			ingresses = append(ingresses, helmIngressFromObject(obj, res))
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
			workloads = append(workloads, helmWorkloadFromObject(obj, res))
		case "Job":
			workloads = append(workloads, helmJobFromObject(obj, res))
		case "CronJob":
			workloads = append(workloads, helmCronJobFromObject(obj, res))
		case "ConfigMap", "Secret", "PersistentVolumeClaim", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "HorizontalPodAutoscaler", "PodDisruptionBudget", "NetworkPolicy":
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s/%s dropped - recreate Kubernetes platform object manually if needed", obj.Kind, obj.Metadata.Name))
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s/%s dropped - unsupported Kubernetes kind", obj.Kind, obj.Metadata.Name))
		}
	}
	if len(workloads) == 0 {
		return nil, fmt.Errorf("helm chart has no translatable workloads")
	}

	ingressHosts := helmIngressHostsByService(ingresses)
	components := make([]manifest.Component, 0, len(workloads))
	for _, wl := range workloads {
		c := helmComponentFromWorkload(wl, services, serviceOrder, ingressHosts, res)
		components = append(components, c)
	}

	m := &manifest.Manifest{}
	if len(components) == 1 {
		m.Component = components[0]
		if m.Name == "" {
			m.Name = components[0].Name
		}
	} else {
		m.Name = sanitizeName(chartName)
		if m.Name == "" {
			m.Name = "helm-app"
		}
		m.Components = components
	}
	res.Manifest = m
	if err := res.Render(); err != nil {
		return nil, err
	}
	return res, nil
}

type helmObject struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   helmMetadata   `yaml:"metadata"`
	Spec       map[string]any `yaml:"spec"`
	Raw        map[string]any `yaml:",inline"`
}

type helmMetadata struct {
	Name        string            `yaml:"name"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

type helmService struct {
	Name     string
	Selector map[string]string
	Ports    []helmServicePort
	Type     string
}

type helmServicePort struct {
	Name       string
	Port       int
	TargetPort string
}

type helmIngress struct {
	Name         string
	HostsBySvc   map[string][]string
	HasPathRules bool
	HasTLS       bool
}

type helmWorkload struct {
	Kind        string
	Name        string
	Labels      map[string]string
	PodLabels   map[string]string
	Replicas    int
	Schedule    string
	Containers  []helmContainer
	Volumes     []helmVolume
	Unsupported []string
}

type helmContainer struct {
	Name         string
	Image        string
	Ports        []helmContainerPort
	Env          map[string]string
	Command      []string
	Args         []string
	CPU          int
	Memory       int
	VolumeMounts []helmVolumeMount
	Unsupported  []string
}

type helmContainerPort struct {
	Name string
	Port int
}

type helmVolume struct {
	Name      string
	Kind      string
	ClaimName string
}

type helmVolumeMount struct {
	Name string
	Path string
}

func decodeHelmObjects(rendered []byte) ([]helmObject, error) {
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	var objects []helmObject
	for {
		var obj helmObject
		err := dec.Decode(&obj)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("parse helm output: %w", err)
		}
		if obj.Kind == "" {
			continue
		}
		objects = append(objects, obj)
	}
	return objects, nil
}

func helmServiceFromObject(obj helmObject, res *Result) helmService {
	svc := helmService{Name: obj.Metadata.Name}
	svc.Type = stringField(obj.Spec, "type")
	svc.Selector = stringMap(obj.Spec["selector"])
	for _, p := range listMaps(obj.Spec["ports"]) {
		sp := helmServicePort{Name: stringField(p, "name"), Port: intField(p, "port"), TargetPort: scalarString(p["targetPort"])}
		if sp.TargetPort == "" && sp.Port > 0 {
			sp.TargetPort = strconv.Itoa(sp.Port)
		}
		svc.Ports = append(svc.Ports, sp)
	}
	if svc.Type != "" && svc.Type != "ClusterIP" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Service/%s type=%s dropped - Blob publishes web services through Traefik", obj.Metadata.Name, svc.Type))
	}
	if len(svc.Selector) == 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Service/%s has no selector - could not associate it with a workload", obj.Metadata.Name))
	}
	return svc
}

func helmIngressFromObject(obj helmObject, res *Result) helmIngress {
	in := helmIngress{Name: obj.Metadata.Name, HostsBySvc: map[string][]string{}}
	if len(listMaps(obj.Spec["tls"])) > 0 {
		in.HasTLS = true
		res.Warnings = append(res.Warnings, fmt.Sprintf("Ingress/%s TLS secret config dropped - Blob manages HTTPS at the edge", obj.Metadata.Name))
	}
	for _, rule := range listMaps(obj.Spec["rules"]) {
		host := stringField(rule, "host")
		httpSpec, _ := rule["http"].(map[string]any)
		for _, path := range listMaps(httpSpec["paths"]) {
			if pathType := stringField(path, "pathType"); pathType != "" && pathType != "Prefix" && pathType != "ImplementationSpecific" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("Ingress/%s pathType=%s dropped", obj.Metadata.Name, pathType))
			}
			if p := stringField(path, "path"); p != "" && p != "/" {
				in.HasPathRules = true
			}
			svcName := ingressBackendServiceName(path["backend"])
			if svcName == "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("Ingress/%s backend without service name dropped", obj.Metadata.Name))
				continue
			}
			if host != "" {
				in.HostsBySvc[svcName] = appendUnique(in.HostsBySvc[svcName], host)
			}
		}
	}
	if len(in.HostsBySvc) == 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Ingress/%s has no host/service rules - dropped", obj.Metadata.Name))
	}
	if in.HasPathRules {
		res.Warnings = append(res.Warnings, fmt.Sprintf("Ingress/%s path rules dropped - Blob routes each hostname to one app", obj.Metadata.Name))
	}
	return in
}

func helmWorkloadFromObject(obj helmObject, res *Result) helmWorkload {
	wl := helmWorkload{Kind: obj.Kind, Name: obj.Metadata.Name, Labels: obj.Metadata.Labels, Replicas: intField(obj.Spec, "replicas")}
	if wl.Replicas == 0 && obj.Kind != "DaemonSet" {
		wl.Replicas = 1
	}
	if obj.Kind == "DaemonSet" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("DaemonSet/%s imported as a daemon component, not one allocation per node", obj.Metadata.Name))
	}
	tmpl, _ := obj.Spec["template"].(map[string]any)
	wl.PodLabels = metadataLabels(tmpl["metadata"])
	if len(wl.PodLabels) == 0 {
		wl.PodLabels = wl.Labels
	}
	wl.Containers, wl.Volumes, wl.Unsupported = helmPodSpec(tmpl["spec"], obj.Kind+"/"+obj.Metadata.Name, res)
	if len(listMaps(obj.Spec["volumeClaimTemplates"])) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("StatefulSet/%s volumeClaimTemplates dropped - create Blob volumes manually", obj.Metadata.Name))
	}
	return wl
}

func helmJobFromObject(obj helmObject, res *Result) helmWorkload {
	wl := helmWorkload{Kind: obj.Kind, Name: obj.Metadata.Name, Labels: obj.Metadata.Labels, Replicas: 1}
	tmpl, _ := obj.Spec["template"].(map[string]any)
	wl.PodLabels = metadataLabels(tmpl["metadata"])
	wl.Containers, wl.Volumes, wl.Unsupported = helmPodSpec(tmpl["spec"], obj.Kind+"/"+obj.Metadata.Name, res)
	return wl
}

func helmCronJobFromObject(obj helmObject, res *Result) helmWorkload {
	wl := helmWorkload{Kind: obj.Kind, Name: obj.Metadata.Name, Labels: obj.Metadata.Labels, Replicas: 1, Schedule: stringField(obj.Spec, "schedule")}
	jobTemplate, _ := obj.Spec["jobTemplate"].(map[string]any)
	jobSpec, _ := jobTemplate["spec"].(map[string]any)
	tmpl, _ := jobSpec["template"].(map[string]any)
	wl.PodLabels = metadataLabels(tmpl["metadata"])
	wl.Containers, wl.Volumes, wl.Unsupported = helmPodSpec(tmpl["spec"], obj.Kind+"/"+obj.Metadata.Name, res)
	if wl.Schedule == "" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("CronJob/%s missing schedule - add schedule: before deploy", obj.Metadata.Name))
	}
	return wl
}

func helmPodSpec(v any, owner string, res *Result) ([]helmContainer, []helmVolume, []string) {
	spec, _ := v.(map[string]any)
	var unsupported []string
	for _, field := range []string{"initContainers", "serviceAccountName", "nodeSelector", "tolerations", "affinity", "securityContext", "imagePullSecrets", "hostNetwork", "dnsPolicy", "priorityClassName"} {
		if _, ok := spec[field]; ok {
			unsupported = append(unsupported, field)
		}
	}
	if len(unsupported) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s pod fields dropped: %s", owner, strings.Join(unsupported, ", ")))
	}
	volumes := helmVolumes(spec["volumes"], owner, res)
	containers := []helmContainer{}
	for _, c := range listMaps(spec["containers"]) {
		containers = append(containers, helmContainerFromMap(c, owner, res))
	}
	if len(containers) == 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s has no containers", owner))
	}
	return containers, volumes, unsupported
}

func helmContainerFromMap(c map[string]any, owner string, res *Result) helmContainer {
	out := helmContainer{Name: stringField(c, "name"), Image: stringField(c, "image"), Env: map[string]string{}}
	out.Command = stringSlice(c["command"])
	out.Args = stringSlice(c["args"])
	if len(out.Command) == 0 && len(out.Args) > 0 {
		out.Command = out.Args
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s container %q has args without command - imported args as Blob command override", owner, out.Name))
	} else if len(out.Command) > 0 && len(out.Args) > 0 {
		out.Command = append(out.Command, out.Args...)
	}
	for _, ev := range listMaps(c["env"]) {
		key := stringField(ev, "name")
		if key == "" {
			continue
		}
		if val, ok := ev["value"]; ok {
			out.Env[key] = fmt.Sprint(val)
			continue
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s container %q env %s uses valueFrom - create a Blob secret or service binding manually", owner, out.Name, key))
	}
	if len(out.Env) == 0 {
		out.Env = nil
	}
	if _, ok := c["envFrom"]; ok {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s container %q envFrom dropped - create Blob secrets manually", owner, out.Name))
	}
	for _, p := range listMaps(c["ports"]) {
		out.Ports = append(out.Ports, helmContainerPort{Name: stringField(p, "name"), Port: intField(p, "containerPort")})
	}
	out.CPU, out.Memory = helmResources(c["resources"])
	for _, vm := range listMaps(c["volumeMounts"]) {
		name := stringField(vm, "name")
		path := stringField(vm, "mountPath")
		if name != "" && path != "" {
			out.VolumeMounts = append(out.VolumeMounts, helmVolumeMount{Name: name, Path: path})
		}
	}
	for _, field := range []string{"livenessProbe", "readinessProbe", "startupProbe", "securityContext", "lifecycle"} {
		if _, ok := c[field]; ok {
			out.Unsupported = append(out.Unsupported, field)
		}
	}
	if len(out.Unsupported) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s container %q fields dropped: %s", owner, out.Name, strings.Join(out.Unsupported, ", ")))
	}
	return out
}

func helmComponentFromWorkload(wl helmWorkload, services map[string]helmService, serviceOrder []string, ingressHosts map[string][]string, res *Result) manifest.Component {
	name := sanitizeName(wl.Name)
	if name == "" {
		name = "app"
	}
	c := manifest.Component{Name: name}
	if len(wl.Containers) == 0 {
		c.Form = "daemon"
		return c
	}
	primary := wl.Containers[0]
	c.Image = primary.Image
	c.Env = primary.Env
	c.Command = primary.Command
	c.CPU = primary.CPU
	c.Memory = primary.Memory
	for _, sc := range wl.Containers[1:] {
		if sc.Image == "" {
			continue
		}
		c.Sidecars = append(c.Sidecars, manifest.Sidecar{Name: sanitizeName(sc.Name), Image: sc.Image, CPU: sc.CPU, Memory: sc.Memory, Env: sc.Env, Args: sc.Command})
	}
	if len(wl.Containers) > 1 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s/%s imported %d extra containers as Blob sidecars", wl.Kind, wl.Name, len(wl.Containers)-1))
	}
	c.Volumes = helmVolumeMounts(primary.VolumeMounts, wl.Volumes, wl.Kind+"/"+wl.Name, res)

	svc, hasService := helmMatchService(wl, services, serviceOrder)
	hosts := []string{}
	if hasService {
		hosts = ingressHosts[svc.Name]
	}
	port := 0
	if hasService {
		port = helmServiceTargetPort(svc, primary)
	}
	if port == 0 {
		port = firstContainerPortNumber(primary)
	}

	switch wl.Kind {
	case "Job":
		c.Form = "job"
	case "CronJob":
		c.Form = "cronjob"
		c.Schedule = wl.Schedule
	case "DaemonSet":
		if port > 0 && (hasService || len(hosts) > 0) {
			c.Form = "web-service"
			c.Port = port
		} else {
			c.Form = "daemon"
		}
	default:
		if port > 0 && (hasService || len(hosts) > 0 || len(primary.Ports) > 0) {
			c.Form = "web-service"
			c.Port = port
			c.Replicas = wl.Replicas
		} else {
			c.Form = "daemon"
			c.Replicas = wl.Replicas
		}
	}
	if len(hosts) > 0 {
		c.Domain = hosts[0]
		if len(hosts) > 1 {
			c.Domains = hosts[1:]
		}
	}
	return c
}

func helmMatchService(wl helmWorkload, services map[string]helmService, order []string) (helmService, bool) {
	for _, name := range order {
		svc := services[name]
		if selectorMatches(svc.Selector, wl.PodLabels) {
			return svc, true
		}
	}
	if svc, ok := services[wl.Name]; ok {
		return svc, true
	}
	return helmService{}, false
}

func selectorMatches(selector, labels map[string]string) bool {
	if len(selector) == 0 || len(labels) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func helmServiceTargetPort(svc helmService, c helmContainer) int {
	if len(svc.Ports) == 0 {
		return 0
	}
	p := svc.Ports[0]
	if n := parsePort(p.TargetPort); n > 0 {
		return n
	}
	for _, cp := range c.Ports {
		if cp.Name == p.TargetPort && cp.Port > 0 {
			return cp.Port
		}
	}
	if p.Port > 0 {
		return p.Port
	}
	return 0
}

func firstContainerPortNumber(c helmContainer) int {
	for _, p := range c.Ports {
		if p.Port > 0 {
			return p.Port
		}
	}
	return 0
}

func helmIngressHostsByService(ingresses []helmIngress) map[string][]string {
	out := map[string][]string{}
	for _, in := range ingresses {
		for svc, hosts := range in.HostsBySvc {
			for _, host := range hosts {
				out[svc] = appendUnique(out[svc], host)
			}
		}
	}
	return out
}

func helmVolumes(v any, owner string, res *Result) []helmVolume {
	var out []helmVolume
	for _, vol := range listMaps(v) {
		name := stringField(vol, "name")
		hv := helmVolume{Name: name}
		switch {
		case vol["persistentVolumeClaim"] != nil:
			hv.Kind = "persistentVolumeClaim"
			pvc, _ := vol["persistentVolumeClaim"].(map[string]any)
			hv.ClaimName = stringField(pvc, "claimName")
		case vol["emptyDir"] != nil:
			hv.Kind = "emptyDir"
		case vol["configMap"] != nil:
			hv.Kind = "configMap"
		case vol["secret"] != nil:
			hv.Kind = "secret"
		default:
			hv.Kind = "other"
		}
		out = append(out, hv)
		if hv.Kind != "persistentVolumeClaim" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s volume %q type %s dropped", owner, name, hv.Kind))
		}
	}
	return out
}

func helmVolumeMounts(mounts []helmVolumeMount, volumes []helmVolume, owner string, res *Result) []manifest.VolumeMount {
	volByName := map[string]helmVolume{}
	for _, v := range volumes {
		volByName[v.Name] = v
	}
	var out []manifest.VolumeMount
	for _, m := range mounts {
		v, ok := volByName[m.Name]
		if !ok {
			continue
		}
		if v.Kind != "persistentVolumeClaim" {
			continue
		}
		name := sanitizeName(firstNonEmpty(v.ClaimName, v.Name))
		if name == "" {
			name = "data"
		}
		out = append(out, manifest.VolumeMount{Name: name, Path: m.Path})
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s PVC volume %q imported as Blob Docker volume %q; storageClass/access modes are not preserved", owner, v.Name, name))
	}
	return out
}

func helmResources(v any) (int, int) {
	res, _ := v.(map[string]any)
	requests, _ := res["requests"].(map[string]any)
	limits, _ := res["limits"].(map[string]any)
	cpu := parseKubeCPU(scalarString(requests["cpu"]))
	if cpu == 0 {
		cpu = parseKubeCPU(scalarString(limits["cpu"]))
	}
	mem := parseKubeMemory(scalarString(requests["memory"]))
	if mem == 0 {
		mem = parseKubeMemory(scalarString(limits["memory"]))
	}
	return cpu, mem
}

func parseKubeCPU(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "m") {
		return parsePort(strings.TrimSuffix(s, "m"))
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int(f * 1000)
}

func parseKubeMemory(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"Ki", 1.0 / 1024.0}, {"Mi", 1}, {"Gi", 1024}, {"Ti", 1024 * 1024},
		{"K", 1.0 / 1000.0}, {"M", 1000.0 / 1024.0}, {"G", 1000.0 * 1000.0 / 1024.0},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			if err != nil {
				return 0
			}
			return int(f * u.mult)
		}
	}
	return parsePort(s)
}

func helmChartName(path string) string {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		b, err := os.ReadFile(filepath.Join(path, "Chart.yaml"))
		if err == nil {
			var c struct {
				Name string `yaml:"name"`
			}
			if yaml.Unmarshal(b, &c) == nil && c.Name != "" {
				return c.Name
			}
		}
		return filepath.Base(path)
	}
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".tgz")
	base = strings.TrimSuffix(base, ".tar.gz")
	return base
}

func listMaps(v any) []map[string]any {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(xs))
	for _, x := range xs {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		out[k] = fmt.Sprint(v)
	}
	return out
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return scalarString(m[key])
}

func intField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		return parsePort(v)
	default:
		return 0
	}
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == float64(int(x)) {
			return strconv.Itoa(int(x))
		}
		return fmt.Sprint(x)
	default:
		return ""
	}
}

func stringSlice(v any) []string {
	xs, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func metadataLabels(v any) map[string]string {
	m, _ := v.(map[string]any)
	return stringMap(m["labels"])
}

func ingressBackendServiceName(v any) string {
	backend, _ := v.(map[string]any)
	service, _ := backend["service"].(map[string]any)
	if service != nil {
		return stringField(service, "name")
	}
	return stringField(backend, "serviceName")
}

func appendUnique(xs []string, x string) []string {
	if x == "" {
		return xs
	}
	for _, cur := range xs {
		if cur == x {
			return xs
		}
	}
	return append(xs, x)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
