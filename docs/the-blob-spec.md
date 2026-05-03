# The Blob — Business Requirements & Technical Spec (v1)

**Status:** Final for v1 implementation
**Date:** 2026-05-03
**Surfaces:** `/blob` Claude Code skill, `blobctl` CLI, HTTP API, web console
**Authoring file:** `blob.yaml`
**Product:** open, self-hosted, agent-native infrastructure that makes all enrolled compute, storage, addresses, secrets, observability, and managed services feel like one invisible resource fabric — one big blob.

This document is the single source of truth for v1 of The Blob. v1 is not an MVP. v1 is a complete, day-one-usable, production-grade platform for solo builders, agentic systems, and small platform teams. Every novel mechanism in this spec has explicit invariants, failure modes, acceptance tests, and cost. Every commodity capability (logs, metrics, traces, backups, autoscaling, preview environments, status pages, web console, GPU, billing, multi-region failover) is in v1. Earlier draft MVP specs and BR documents are superseded.

---

## Part I — Business requirements

### 1. What The Blob is

The Blob is a self-hosted platform that turns a fleet of mixed hardware — Raspberry Pis, cheap VPSes, laptops, bare-metal boxes, GPU rigs, and confidential-compute nodes — into a single resource pool a person or an agent can deploy into without thinking about which machine, which disk, which IP address, or which Kubernetes object will eventually represent the work. The user stands in a project folder and says “deploy this.” The platform inspects the folder, builds or imports the workload, writes a canonical manifest, schedules an isolated machine, attaches storage, injects secrets, publishes routes, generates dashboards and a runbook, sets up backups, and records audit evidence.

The Blob competes with Fly.io on deploy experience, with Render and Railway on managed services, with Upsun and Vercel on multi-component app ergonomics, with Coolify on self-hosting, and with raw Kubernetes on operational power. It does not feel like Kubernetes to its users. The substrate underneath is real Kubernetes (k3s) plus real CSI plus real Envoy plus real Longhorn plus real DNS plus real Prometheus plus real Loki plus real Tempo — but the user does not have to know that, and the platform owns every projection from manifest to runtime.

### 2. The promise

A user has a folder. They run `/blob` or `blobctl deploy`. They get a running workload with an owner, backup owner, manifest, endpoint, logs, metrics, traces, alerts, dashboards, rollback, secrets, storage, backups, status page entry, cost record, and runbook. A different operator can take that workload over from those generated artifacts without asking the original builder how it works. If the user has no fleet, `blobctl provision` buys a node from a provider, joins it, and deploys onto it. If the user has a folder full of `docker-compose.yaml`, a `fly.toml`, a Helm chart, or a `flake.nix`, the importer translates it into The Blob’s canonical manifest before deploying. If the workload outgrows one node, autoscaling and multi-region failover already exist — they are not a future-paid add-on.

### 3. Who it is for

- **Solo builders and small teams** who want Fly.io / Render ergonomics on hardware they own or rent directly.
- **Agentic systems** that spin up, modify, hand off, and tear down workloads without the friction of cloud consoles.
- **Internal platform teams** that want a Coolify-class self-hosted product without the limits, with real RBAC, audit, secrets, observability, backups, and managed services.
- **Operators inheriting unfamiliar workloads** — handoff-by-runbook is a first-class workflow, not an afterthought.
- **Regulated teams** that need a self-hosted platform with audit anchoring, data residency, confidential compute, and signed-publisher policies, without paying for an enterprise add-on.

### 4. v1 scope: a complete platform, not an MVP

v1 ships every capability a reasonable team expects from a modern PaaS. The platform is judged complete only when all of the following are part of the same product, on the same code paths, accepted by the same gates:

- One-command deploy from any project folder, via skill or CLI.
- Mixed-hardware fleet management with provider purchasing.
- Microvm-isolated workloads, plain VMs, browser agents, GPU workloads, confidential workloads, scheduled jobs, one-shot jobs, and multi-component apps.
- Block, file, and object storage, with snapshots, backups, encryption, and opt-in rewind.
- Public HTTP, public TCP/UDP, internal addressing, Cloudflare Tunnel, and platform-base / user-managed / user-external domains.
- Native Raft secret store and external secret backends.
- Identity (WebAuthn, OIDC, deploy tokens, CI OIDC, signed webhooks), RBAC, and tamper-evident audit.
- First-class observability: logs, metrics, traces, dashboards, alerts, SLOs, error tracking.
- Autoscaling: traffic-driven, queue-depth-driven, and schedule-driven, with scale-to-zero where safe.
- Backups and point-in-time recovery for managed services and Volumes; documented disaster-recovery procedures.
- Multi-region deploys with active-passive failover and a managed cutover workflow.
- CI/CD with deploy tokens, OIDC federation, GitOps mode, signed webhooks, and per-PR preview environments.
- Web console covering catalog, runbooks, deploys, rollbacks, secrets, observability, costs, audit, and access reviews.
- Cost management with per-workload, per-team, per-org rollups, budgets, alerts, and showback/chargeback exports.
- Status pages, uptime monitoring, and external probes.
- Plugin model for runtimes, providers, secret backends, observability sinks, importers, and managed services.
- Migration tools: import from Compose, Kubernetes, Helm, Fly.io, Procfile, Heroku, Render, Vercel/Netlify, Cloudflare Workers, Nix flakes; export to portable manifests.
- SDKs in Node, Python, Go, and Rust.
- Documented compliance posture (data residency, retention, attestation, signed-publisher policy).

### 5. Non-goals for v1

The following are explicitly out of scope. They are not deferred to v2; they are decisions, not omissions.

- Operating a public multi-tenant PaaS for unknown tenants.
- Cross-region active-active replication for application data (active-passive failover is in scope).
- Traffic-driven autoscaling for stateful managed services beyond read-replica scaling.
- General Kubernetes self-service to end users.
- A template marketplace beyond pilot examples and first-party SDK starters.
- Building a new distributed filesystem from scratch.

### 6. Business-level requirements

| ID | Requirement | Why it matters |
|---|---|---|
| BR-1 | One-command deploy from a project folder via `/blob` or `blobctl` | The product collapses if the deploy path is not effortless |
| BR-2 | Self-hosted, open-source defaults; no required SaaS dependencies | Audience demands sovereignty over data and runtime |
| BR-3 | Permissive licenses (Apache-2.0 / MIT / BSD) for every default component | BSL/AGPL surprises kill self-hosted adoption |
| BR-4 | Every workload has owner, backup owner, runbook, lifecycle tag, support tier, dashboards, alerts, and audit trail | Handoff and on-call are core workflows, not optional |
| BR-5 | `/blob`, `blobctl`, web console, and API are equal clients; identical inputs produce identical state | Agent, human-CLI, and human-GUI surfaces must not diverge |
| BR-6 | The platform refuses workloads it cannot honor instead of silently degrading | Trust depends on “no surprise failure modes” |
| BR-7 | A non-builder can take over any workload using only generated artifacts | The platform’s value collapses if knowledge lives only in the builder’s head |
| BR-8 | Hardware floor: a Raspberry Pi 4 (4 GiB) can run a control-plane node in `core` profile | Cheap-hardware-first is the differentiating promise |
| BR-9 | Profiles (`solo`, `team`, `enterprise`) set defaults; they do not fork code paths | One product, not three products with the same name |
| BR-10 | Imports from Compose, Kubernetes, Helm, Fly.io, Procfile, Heroku, Render, Vercel/Netlify, Cloudflare Workers, and Nix flakes | The audience already has work in those formats |
| BR-11 | First-class agent surfaces (manifest, query, deploy, rollback, doctor, autoscale, rewind) | The product is built for agents to operate, not just humans |
| BR-12 | Costs and capacity observable per workload, team, and org from day one | Self-hosted does not mean cost-blind |
| BR-13 | Logs, metrics, traces, and alerts are wired by default for every workload | A workload without observability is operationally invisible |
| BR-14 | Backups and point-in-time recovery exist for every Volume class and managed service | Loss of state is the worst failure; v1 does not ship without recovery |
| BR-15 | Multi-region active-passive failover with documented cutover and tested drill | Single-region promises do not survive a real outage |
| BR-16 | CI/CD with preview environments per PR and OIDC-federated deploys | Modern teams expect this; deploy-token-only is not enough |
| BR-17 | Status page and uptime monitoring generated per workload and per environment | External users need a public truth surface during incidents |
| BR-18 | Plugin model for runtimes, providers, secret backends, observability sinks, importers | The platform must extend without forking |
| BR-19 | Compliance posture documented: residency, retention, attestation, audit anchoring | Regulated teams need this in v1, not later |
| BR-20 | Web console covering everything the CLI does, with the same auth and audit | A CLI-only product loses operators who think visually |

### 7. Top user scenarios

These scenarios must work end-to-end on day one. Every other surface is in service of them.

1. **Cold-start solo user.** A developer on a laptop installs `blobctl`, runs `blobctl provision --provider hetzner --plan cax11`, gets a control-plane node, runs `blobctl deploy` in a Node.js project folder, and reaches an HTTPS URL within 60 seconds of the node being reachable. Logs, metrics, traces, dashboard, and runbook are already wired.
2. **Agent-driven deploy.** An agent calls `/blob` from inside a Claude Code session, points at a folder, and ships the workload with a generated runbook, dashboards, alerts, owner metadata, and a backup schedule. The agent can `blobctl rollback`, `blobctl logs`, `blobctl trace`, `blobctl doctor`, `blobctl autoscale set`, and `blobctl handoff` without human intervention.
3. **Compose import.** A team with a `docker-compose.yaml` runs `blobctl import compose ./docker-compose.yaml`. The platform produces a canonical manifest, lists what was preserved and what is unsupported, shows the deploy diff, and only then deploys.
4. **Multi-component app.** An app with a web service, a worker, a Postgres database, a Valkey cache, and a NATS stream is described as a single `App` manifest and deploys as one unit. Component changes redeploy only the changed component. The app gets one logical status page and one logical dashboard.
5. **Fleet expansion.** A team running on three Pis adds a Hetzner VPS through `blobctl provision`. New workloads land on whichever node fits; existing workloads are unaffected. The new node’s cost shows up in the billing rollup.
6. **Handoff.** A workload’s owner leaves the team. `blobctl handoff link-checker --to platform-lead@example.com` transfers ownership, preserves the service identity, kills the original owner’s sessions, regenerates the runbook from current state, and produces an audit record.
7. **Rewind.** An agent deletes rows from a small Postgres instance. The on-call operator runs `blobctl db rewind support-pg --to "2026-04-30T14:02:00Z"`, validates the new instance, and either promotes it or exports the missing data.
8. **Public exposure with custom domain.** A workload requests a public domain on a registrar the platform cannot manage directly. The platform returns the routing record, ACME CNAME, and verification TXT to create. After the user creates them, the cert provisions and routing comes up. Bindings survive redeploy, rollback, and node migration.
9. **Browser agent on a NAT/laptop node.** A workload that drives Playwright runs as a Bundle with a Cloudflare Tunnel sidecar from a NAT/laptop node. Tunnel credentials are injected as secrets. The workload is observable like any other.
10. **Drift caught.** Someone edits a Kubernetes Service object directly. `blobctl doctor` raises a P1 because the projection hash no longer matches the manifest hash, names the actor, and prints the remediation command.
11. **Preview environment.** A developer pushes a branch. CI calls The Blob via OIDC. The Blob spins up a preview environment with isolated secrets, scoped data, a temporary subdomain, and a dashboard. When the PR closes, the environment is destroyed and audited.
12. **Autoscaled spike.** A queue-driven worker sees a 50× message spike. Autoscaling provisions additional Machines on warm blebs within seconds; the queue drains; replicas scale back down; cost is recorded against the team’s budget; an alert fires only if the budget threshold trips.
13. **Region failover drill.** A `team`-profile cluster runs in two regions, active-passive. The operator runs `blobctl failover --to region-b --reason drill`. Traffic shifts, secrets and Volumes are already replicated, the cutover is timed, and a drill report is filed.
14. **Cost rollup and budget alert.** An org admin opens the cost view. They see per-workload, per-team, per-region rollups, identify the noisy worker, set a per-team budget cap, and confirm that breaching the cap fires an alert and pauses non-critical scale-out.
15. **Status page during incident.** A workload becomes unhealthy. `blobctl status incident open` files a public incident on the workload’s status page; doctor scopes the affected components automatically; the on-call operator updates the incident from CLI; the status page reflects updates within 30 seconds.

### 8. Success metrics

- A fresh `solo` user logs in, deploys a Dockerfile service, and reaches a working platform-base HTTPS URL within 60 seconds of cluster availability.
- Median warm redeploy ≤ 5 s; p95 ≤ 10 s.
- Cold first deploy of a small service ≤ 30 s p50, ≤ 60 s p95.
- Rollback ≤ 3 s p50.
- Per-PR preview environment up and reachable ≤ 90 s p95 from the CI webhook.
- Region failover drill cutover ≤ 5 minutes p95 and a documented evidence file produced automatically.
- Backup recovery test for every Volume class and managed service passes monthly in CI.
- A non-builder operator completes every pilot handoff drill using only the generated runbook, manifest, dashboards, and `blobctl` queries.
- `blobctl doctor` returns zero issues on a clean fleet and the exact expected issues on broken fixtures.
- A Pi 4 (4 GiB) successfully bootstraps a `core` profile control plane and serves a Dockerfile workload at a public HTTPS URL.

---

## Part II — Technical specification

### 9. Primitives

The user-facing primitives are deliberately small. v1 ships all of them.

- **Machine** — one isolated workload instance. By default it runs inside a microVM-class runtime (Kata + Cloud Hypervisor). Plain container execution exists but is explicit, labeled, and blocked for production-like workloads unless policy allows.
- **Bundle** — a co-scheduled group of Machines that share a network namespace and, optionally, a Volume scope. Sidecars, tunnel connectors, browser helpers, and tightly coupled Compose units map to Bundles.
- **Volume** — storage requested by shape, not by node. Block, file, or object, with size, durability, backup policy, performance class, and optional rewind. The scheduler decides locality and backend.
- **Domain** — hostname ownership. Platform-base, user-managed via DNS-provider API, user-external (BYO-DNS), or Cloudflare Tunnel.
- **Secret** — typed, versioned, scoped value. Values never appear in manifests, registry records, logs, runbooks, or dashboards.
- **Managed Service** — a provisioned database, cache, object bucket, or event stream whose credentials, backups, rewind, observability, and cost records are generated by the platform.
- **App** — a multi-component workload composed of Machines, Bundles, Volumes, Domains, Secrets, and Managed Services with declared dependencies.
- **Environment** — a named deployment context (e.g. `prod`, `staging`, `pr-1234`). Environments share manifests but isolate secrets, data, and routing.
- **Plugin** — a registered extension that adds a runtime class, a provider, a secret backend, an observability sink, an importer, or a managed-service driver.

Workloads come in a small set of generic forms. Every deployment is one of these — the platform does not carry domain-specific shapes:

- **Web service** — long-running HTTP/HTTPS server, optionally public, optionally autoscaled, optionally scale-to-zero.
- **Daemon** — long-running process with no inbound traffic (workers, consumers, schedulers, polling agents, browser automations).
- **Function** — short-lived, event-triggered handler that scales from zero on demand. Triggers include HTTP, queue messages, object-storage events, schedules, and webhooks.
- **Scheduled job** — recurring task on a cron expression with the same isolation, observability, and audit guarantees as a service.
- **One-shot job** — single execution, run once and recorded. Useful for migrations, batch processing, and ad-hoc tasks.
- **VM** — a plain virtual machine you SSH into. No app model imposed; The Blob handles placement, storage, networking, secrets, backups, and observability around it.

`MachineService` is the manifest kind for web services and daemons; `Function` for functions; `Job` and `CronJob` for one-shot and scheduled tasks; `MachineService` with `mode: vm` for plain VMs.

`/blob`, `blobctl`, the API, and the web console are equal clients. All four call the same API and must produce byte-for-byte equivalent registry state for the same input. Direct edits to Kubernetes objects, Envoy routes, NetworkPolicies, dashboards, or any other projection are drift, and `blobctl doctor` treats drift as a P1 issue.

### 10. Hardware profiles and floors

| Profile | Master floor | What runs |
|---|---|---|
| `ultralight` | 2 GiB RAM, 1–2 vCPU, SSD strongly preferred | API, registry metadata, k3s, Flannel, one secret replica. No managed services, no warm bleb pool, minimal observability. Suitable for learning and very small homelabs |
| `core` | Pi 4 4 GiB or comparable VPS | Full deploy path, microVM runtime, Longhorn block, SeaweedFS object/file, registry, ingress, secrets, doctor, observability stack, autoscaling, preview environments, backups |
| `full` | 3+ stable nodes, 8 GiB+ each on storage nodes | HA control plane, multi-region failover, managed services (including ScyllaDB), optional Ceph, chaos schedule, full observability, GPU and confidential workloads |
| `enterprise` | Customer-defined | Same code paths as `full`; stricter defaults; external WORM/HSM/SIEM integrations; required signed-publisher policy |

A 1 GiB node can be a worker; it is not a supported full master. The product remains cheap-hardware-first, but the spec stops pretending RAM is free.

### 11. Architecture

The Blob has three layers.

**Client layer** — `/blob`, `blobctl`, web console, GitOps reconciler, CI action, importers, SDKs. Clients inspect projects, produce manifests, call preflight, and present plans.

**Control layer** — API, identity/RBAC, registry, audit log, secret resolver, placement engine, provider catalog, manifest projector, doctor, observability backplane, billing aggregator, plugin host. Small by design. No workload data path runs in process.

**Node layer** — k3s, containerd, Kata Containers, KubeVirt (when enabled), storage agents, network agents, secret CSI, gossip agent, lazy-pull snapshotter, log shipper, metric scraper, trace forwarder, L4 forwarder, L7 proxy, GPU driver shims.

End-to-end deploy flow:

```
project folder / imported manifests
  → inspector / importer
  → canonical manifest (blob.yaml)
  → preflight: identity, policy, secret refs, storage, exposure, trust, budget
  → placement plan from the resource graph
  → projections: runtime objects, routes, policies, dashboards, alerts, runbook, status page entry, backup schedule, cost record
  → deploy
  → doctor continuously verifies projection hashes and live health
```

### 12. Final technical decisions

| Area | Decision | Why | Cost |
|---|---|---|---|
| Scheduler / runtime | k3s + Kubernetes API hidden behind The Blob control plane | k3s is lightweight, ARM-friendly, and gives scheduling, jobs, networking, CSI, CRDs, and migration inputs | The Blob owns all projection and drift detection; operators still run a Kubernetes substrate |
| Machine isolation | Kata Containers with Cloud Hypervisor; QEMU path for confidential runtime classes | Kata runs containers in lightweight VMs, integrates with containerd / Kubernetes, supports multiple hypervisors | Cold-start tuning is real work; container-only is not the default |
| Plain VMs | KubeVirt, installed only when VM mode is enabled | KubeVirt manages VMs on top of Kubernetes, sharing placement, storage, identity rails | Adds CRDs/controllers; disabled on tiny fleets unless VM mode is requested |
| Network baseline | Flannel / WireGuard in `ultralight`; Cilium in `core` / `full` when kernel support is present | Flannel keeps Pi and cheap VPS support viable; Cilium provides eBPF policy and kube-proxy replacement when the kernel can handle it | Two network profiles must be tested |
| L7 routing | Envoy xDS owned by The Blob | Route changes apply immediately; no slow ingress reconciliation | The Blob owns route generation and cert integration |
| Public TCP/UDP | XDP/eBPF fast path with nftables fallback | XDP runs before socket-buffer allocation; appropriate for high-PPS forwarding | Kernel capability detection, BPF verifier tests, fallback performance labeling |
| Block storage | Longhorn in `core`; Rook/Ceph option in `full` | Longhorn is lightweight distributed block storage with synchronous replication; Ceph gives unified block/file/object at scale | Longhorn file/object alone are not enough; Ceph is too heavy for tiny installs |
| File storage | Longhorn RWX/NFS for simple shared file Volumes; SeaweedFS file/FUSE for large object-like file sets; CephFS only in `full` | Supported path across small and large fleets | Strong POSIX concurrency at high write rates is `full`-only |
| Object storage | SeaweedFS S3 in `core`; Ceph RGW optional in `full` | Apache-2.0, S3-compatible, light footprint | Not the source of strong POSIX semantics |
| Artifact store | OCI-Distribution registry, default `zot`, with ORAS for non-image artifacts | OCI registries store content-addressed blobs and support image and non-image artifacts | Nix binary-cache semantics need an adapter, not blind OCI abuse |
| Image speed | eStargz or Nydus lazy-pull snapshotter; Spegel-style peer cache where profile allows | Lazy pulling lets containers start before full image download; peer cache reduces registry dependency | Every peer-served layer must be signature-verified |
| Signing | Sigstore / cosign preflight and runtime verification | Industry-standard image signing | Trusted publisher policy is profile-driven; unsigned dev images need an explicit exception path |
| Secrets | Native Raft-backed encrypted secret store by default; external drivers for Vault, AWS/GCP/Azure, SOPS, 1Password, Doppler, age | Works on homelab and enterprise; secrets stay references in manifests | Native store must handle seal/unseal, replication, rotation, migration |
| KV / cache | Valkey default; Dragonfly optional where BSL is acceptable | Valkey is BSD-licensed; Dragonfly is BSL and cannot be the default open self-hosted cache | Dragonfly is an optional performance tier, not baseline |
| Event streams | NATS JetStream default; Redpanda optional only | NATS is Apache-2.0 and lightweight; Redpanda Community is BSL | Kafka-compatible workloads need an import/bridge story, not default Redpanda |
| Postgres | CloudNativePG | Mature cloud-neutral Postgres operator with HA lifecycle and PITR | Tiny single-node mode still needs a cheap single-instance path |
| ScyllaDB | `full`-profile managed service | Valuable, but too heavy for Pi-class fleets | `/blob` must refuse ScyllaDB on undersized fleets with a clear error |
| Logs | Vector → Loki | Lightweight collection, scalable storage, queryable from console and CLI | Loki indexes are coarse; full-text needs an opt-in index sink |
| Metrics | Prometheus-compatible scraping → VictoriaMetrics | Single-binary, low-RAM TSDB, Prometheus-compatible reads | Long-term retention belongs in object storage |
| Traces | OpenTelemetry Collector → Tempo | Standard OTLP ingest; cheap object-backed storage | High-cardinality trace search needs sampling guidance |
| Error tracking | GlitchTip (Sentry-compatible) | OSS, MIT-licensed, Sentry SDK compatibility | Must be wired by default, not opt-in |
| Dashboards / alerts | Grafana with platform-managed dashboards and Alertmanager rules generated from manifests | Industry-standard, scriptable | Dashboards must be regenerable; user-edited dashboards are drift |
| Status pages | Built-in, per workload and per environment, served from the control plane | External users need a truth surface during incidents | Must integrate with doctor, on-call, and audit |
| Autoscaling | KEDA-style event-driven scaling and a built-in HTTP RPS scaler | Covers queue-driven and traffic-driven workloads | Scale-to-zero must respect cold-start budgets and warm bleb pool |
| Backups | Velero for cluster state; CloudNativePG PITR for Postgres; Longhorn snapshots for Volumes; rclone-based off-cluster shipping | Established components, off-site by default | Restore drills are part of the release gate, not operator hope |
| Multi-region | Active-passive at the platform level; per-workload replication policy | Single-region is unsafe for any workload that matters | Active-active is out of scope for v1; failover drills are required |
| CI/CD | Deploy tokens, CI OIDC federation, signed webhooks, GitOps mode, preview environments per PR | Teams already deploy from CI; preview environments are table stakes | More auth paths, same audit/eval path |
| Domains | Platform-base, user-managed DNS provider, user-external BYO-DNS, Cloudflare Tunnel | Covers no-domain, DNS-provider, registrar-only, NAT/laptop nodes | ACME quota management and DNS delegation required |
| GPU / AI | NVIDIA device plugin, MIG slicing, AMD ROCm where supported, model-server runtime class with KV-cache hints | AI workloads are first-class in 2026 | Must refuse GPU asks on non-GPU fleets with a clear error |
| Confidential | SEV-SNP and TDX runtime classes via Confidential Containers | Regulated teams need this | Must refuse non-confidential placement and require attestation records |

### 13. Canonical manifests

Every workload has one canonical manifest. `blob.yaml` is the project-root authoring file. The control plane normalizes it into one of these resource kinds:

- `MachineService` — web service, daemon, browser agent, GPU workload, or plain VM (selected by the `form` field).
- `Function` — event-triggered short-lived handler.
- `Job` — one-shot task.
- `CronJob` — scheduled task.
- `Bundle` — sidecar / co-scheduled group.
- `App` — multi-component system.
- `Volume` — state.
- `Domain` — hostname ownership and routing.
- `SecretRef` — secret references (never values).
- `ManagedService` — Postgres, Valkey, ScyllaDB, object bucket, event stream.
- `Environment` — named deployment context.
- `Plugin` — registered extension.

A minimal web-service manifest:

```yaml
apiVersion: blob.dev/v1
kind: MachineService
metadata:
  id: link-checker
  name: Link Checker
  org: acme
  team: platform
  owner: nina@example.com
  backupOwner: platform-lead@example.com
  lifecycle: production
  supportTier: team-supported
  runbook: docs/runbooks/link-checker.md
spec:
  form: web-service           # web-service | daemon | function | job | cronjob | vm
  isolation: kata
  image:
    ref: registry.blob.local/platform/link-checker
    digest: sha256:...
    signaturePolicy: team-default
  resources:
    maxCpu: null
    maxMemory: 2Gi
    replicas: { min: 1, max: 5 }
  autoscale:
    mode: rps
    target: 200
  health:
    readiness: { http: /readyz }
    liveness: { http: /healthz }
  exposure:
    profile: public-http
    domain: links.acme.example
  egress:
    mode: profile-default
    allow: ['*']
  data:
    classification: internal
  observability:
    logs: { level: info }
    metrics: { scrape: /metrics, port: 9100 }
    traces: { otlp: true, sampleRate: 0.1 }
    alerts: defaults
    statusPage: public
  backups:
    schedule: defaults
    retention: 30d
  secrets:
    - env: API_TOKEN
      ref: secret://team/platform/link-checker/api-token
      class: api-token
  volumes: []
```

The resource model is descriptive. `maxCpu` and `maxMemory` are ceilings, not plans. Absence means “use available node capacity within platform guards.” Storage always declares size, durability, backup, and performance class because storage promises are impossible without those fields. Observability and backup blocks default to platform-managed dashboards, alerts, and schedules — not absent.

### 14. Mechanism — the resource graph

Each node agent publishes signed resource deltas: CPU and memory headroom, architecture, runtime classes, kernel capabilities (XDP, eBPF, KVM, SEV-SNP/TDX), image cache contents, hot Volume residency, storage capacity by class and failure domain, public IP / port bindings, GPU and confidential-compute capabilities, failure domain, and gossip lag. The control plane stores these deltas in a CRDT-backed graph. Placement evaluates constraints over the graph locally during the deploy path.

A placement succeeds only if one slice satisfies every hard constraint: runtime class, architecture, image trust, team quota, storage durability, storage performance floor, secret backend policy, egress policy, domain / public-port availability, failure domain, data classification, and budget. Soft constraints are locality, image cache hit, hot-tier residency, cost, and anti-flap weight.

The graph is eventually consistent. The invariant is **not** “the graph is perfect.” The invariant is **“no deploy is marked ready until runtime reconciliation proves the claim.”** Stale graph data may cause a placement retry; it must never produce an unsafe placement.

Acceptance tests:

- Placement never queries individual nodes during the warm deploy timer.
- Stale nodes are weighted down after missed gossip epochs.
- A node flapping every 5 seconds receives no new production-like placements.
- A workload requiring local Volume fast path is refused when the graph cannot satisfy locality.
- A workload requiring GPU/confidential capabilities is refused on incapable nodes with a precise error.

### 15. Mechanism — hot journal volumes

Longhorn, Ceph, and object stores do not give local-disk performance for every workload. The Blob adds a hot journal layer in front of the durable backend.

For a block Volume, The Blob exposes a thin encrypted block device to the Machine. Writes append to a local NVMe journal and, for replicated durability, to at least one peer journal. The write is acknowledged only after the required journal `fsync`s complete. The backend flushes journal segments to Longhorn / Ceph / SeaweedFS in order. Reads check the local journal index and hot cache first, then the durable backend. Crash recovery replays committed journal segments. Rewind uses the same segment metadata.

Three modes:

| Mode | Ack condition | Use case |
|---|---|---|
| `local-single` | local journal `fsync` | Experimental, disposable, clearly data-loss-prone on node death |
| `local-plus-peer` | local journal `fsync` + one peer journal `fsync` | Default replicated fast path |
| `backend-sync` | durable backend confirms write | Production databases that cannot tolerate write-back semantics |

For file and object Volumes, the first release applies the hot journal as read cache and object/version log. File writes that need strong shared semantics route through NFSv4 / CephFS. The Blob does not promise high-rate multi-writer POSIX on the `core` profile.

Hot journal is gated on these tests:

- Kill the Machine after ack, before backend flush; committed writes replay.
- Kill the local journal node after ack; peer journal recovers committed writes.
- Partition the peer journal; writes degrade to a declared mode or fail — never silently weaken durability.
- Fill the journal; workload backpressures with an explicit error path, not data loss.
- Sustained random writes above the published hybrid threshold are refused at preflight.

### 16. Mechanism — rewind as a Volume primitive

Rewind is opt-in. It is not backup and it is not enabled by default.

When enabled, hot journal segments and backend change records are chunked, encrypted with the source Volume key, stored in the artifact store, and indexed by Volume / time. `blobctl volume rewind <volume> --to <time>` creates a new Volume at that point in time. In-place restore exists but requires explicit confirmation and approval for production-like Volumes.

Granularity is per-second for the most recent hour and per-minute beyond that. Crash-consistent rewind is required for all supported Volume kinds. App-consistent rewind requires a `quiesce` hook.

Rewind storage grows with write volume. Each org and team has a rewind budget. When a budget fills, the oldest rewind window expires and the event is audited. Backups and snapshots continue unaffected.

### 17. Mechanism — manifest projection hash

Every generated artifact carries three annotations: `sourceManifestHash`, `projectedAt`, `projectedBy`. The reconciler periodically recomputes the manifest hash and compares it to each projection.

If the registry says a service is internal-only but a live Envoy route exposes it publicly, the route hash no longer matches the source. `blobctl doctor` raises a P1 issue that names the manifest, projection, actor (if known), and remediation command. Operators lose the habit of quick direct edits to Kubernetes objects. The payoff is that The Blob can rebuild from manifests and drift is visible.

### 18. Mechanism — blebs (the warm microVM pool)

Fast deploys need warm sandboxes, but warm sandboxes cannot leak tenant state. The Blob uses **blebs**: clean warm cells, never pre-running user workloads.

A bleb contains a minimal kernel, the Kata guest agent, verified rootfs metadata, and page-cache hints for one or more image digests. A bleb does not contain secrets, tenant-writeable disk state, network identity, or service credentials. Claiming a bleb attaches a fresh overlay, injects secrets through CSI after policy approval, binds the network identity, and starts the workload entrypoint. Releasing a bleb destroys or zeroes tenant-writable state before returning it to the pool.

Blebs cost memory. Each node has a capped bleb budget. `ultralight` defaults to zero or one. `core` defaults to a small per-node budget tuned to image diversity. `full` allows larger budgets and per-team reservations. Autoscaling consumes blebs preferentially; cold deploys fall back to fresh microVM creation.

### 19. Identity, RBAC, and audit

The identity model has organizations, teams, users, service identities, roles, grants, and conditions. Every workload and managed service receives a service identity. User offboarding revokes user sessions and keys, but service identities continue running and transfer to the backup owner or team owner.

Authentication supports WebAuthn / passkey native users, GitHub / Google quick attach for small teams, OIDC for organizations, TOTP fallback, deploy tokens, CI OIDC federation, and signed webhooks.

RBAC actions come from a fixed vocabulary. Grants are `(principal, role, scope, conditions, expires_at)`. Conditions include MFA, source network, time, approval, and expiry. Deny overrides allow. The evaluator expands wildcards at grant write-time and indexes by `(principal, resource, verb)` so the deploy hot path does not scan grants.

Audit is append-only and hash-chained. Every mutation records actor, service identity (where applicable), role used, source, action, target, request ID, source IP, before/after diff, outcome, and approval ID. Profile determines anchoring: local detached signature for `solo`, external WORM for `team`, customer WORM/SIEM for `enterprise`. Access reviews run on schedule and produce signed evidence.

### 20. Profiles

Profiles set defaults, never code paths.

| Setting | Solo | Team | Enterprise |
|---|---|---|---|
| Auth | WebAuthn + GitHub/Google quick attach | WebAuthn + optional OIDC | OIDC required except break-glass |
| MFA | Optional except owner / admin | Required for owner / admin | Required for all roles |
| Secrets | Single replica allowed | 3-replica Raft when nodes allow | 3/5 replica + sealed mode required |
| Audit retention | 90 days, local hash-chain verify | 1 year, WORM anchor | 7 years, customer WORM + SIEM |
| Image trust | Permissive signed images | Trusted publisher list | Non-empty customer publisher list |
| Egress | Allow with logging | Production-like deny by default | Deny by default, IP-pinned for restricted |
| Public exposure approval | Off by default | Production-like requires approval | Full sensitive-action list |
| Peer image cache | On with verification | On with reputation | Off for confidential / restricted |
| Vulnerability scanning | Off unless workload opts in | Opt-in; warn policy available | Required for production-like |
| Chaos | Off | Opt-in scheduled | Practiced against approved targets |
| Backups | Local snapshots + optional off-site | Off-site mandatory, monthly restore drills | Off-site mandatory, weekly restore drills, customer-owned destination |
| Multi-region | Optional | Recommended; failover drill quarterly | Required; failover drill monthly |
| Status pages | Internal by default | Public per workload, opt-in | Public required for production-like |

Profile downgrades require approval and produce high-priority audit events.

### 21. Secrets

The native secret store is a small Raft-replicated encrypted store. Metadata in Raft, values encrypted with per-secret keys, TPM/HSM/KMS wrapping where available. Single-node `solo` installs can use a software-key fallback with an explicit warning; `enterprise` cannot.

External drivers support Vault, AWS Secrets Manager / KMS, GCP Secret Manager, Azure Key Vault, SOPS-over-Git, 1Password Connect, Doppler, and age file backends. The resolver maps `secret://...` references to a backend by org / team policy and secret class.

Secret classes carry hard floors:

- A model-provider key rotates at least every 90 days.
- A third-party API token rotates at least every 180 days.
- A personal credential requires approval and 30-day rotation.
- TLS private keys are generated by cert management and never manually revealed.

Secrets mount via CSI as files or env vars. Values never appear in manifests. `secret:reveal` to a human is separate from `secret:read` by a Machine and requires approval for confidential / restricted classes.

### 22. Networking, domains, and exposure

Exposure profiles are generic and describe how a workload meets the network, not what it does:

- `none` — no inbound traffic. The default for daemons, workers, and most jobs.
- `internal-http` — reachable inside the cluster only, on a stable internal hostname.
- `internal-shareable` — reachable through a signed shareable URL for humans on the team, no public DNS.
- `public-http` — public HTTPS through the L7 proxy, with a managed cert.
- `public-tcp-udp` — public raw TCP/UDP through the L4 forwarder with a stable IP/port lease.
- `public-tunnel` — public HTTPS through Cloudflare Tunnel, suitable for NAT/laptop nodes.
- `vm-ssh` — SSH access to a plain VM, key-based, audited.

Public HTTP uses Envoy and ACME certificates. Raw TCP/UDP uses the L4 forwarder with stable IP/port leases. Bindings survive redeploy and rollback. Migration rebuilds forwarding maps without changing user-visible addresses.

Domains have three modes:

- **`platform-base`** — The Blob owns the base zone and issues `<workload>-<team>.<basedomain>` automatically.
- **`user-managed`** — User supplies a DNS provider token; The Blob verifies ownership, writes records, renews certs.
- **`user-external`** — The Blob cannot access the user’s DNS. It returns records to create: routing record, verification TXT, and `_acme-challenge` CNAME delegation into The Blob’s ACME zone. The platform polls until verified, then manages renewal.

Cloudflare Tunnel is available for Cloudflare-managed domains and NAT/laptop nodes. The connector runs as a Bundle member or per-node connector pool. Tunnel credentials are stored as secrets.

ACME quota management is required: wildcard certs where appropriate, staging endpoints during tests, and cert batching to avoid the 50-certificates-per-registered-domain-per-week limit.

Anycast, BGP-attached IP pools, and per-region load-balancer integration are supported on `full` and `enterprise` profiles. `core` uses provider load balancers or direct node IPs.

### 23. Packaging, import, and Nix

The inspector detects Dockerfile, Compose, Procfile, Kubernetes manifests, Helm charts, Fly.io `fly.toml`, Heroku `app.json`, Render `render.yaml`, Vercel/Netlify build outputs, Cloudflare Workers, cloud-init, language stacks (Node, Python, Go, Rust, Ruby, PHP, Java, .NET, Elixir), scheduled jobs, browser hints, GPU hints, and Nix inputs.

Nix support is first-class but not required. A `flake.nix` may produce an OCI image, a layered image, a runnable derivation wrapped into an OCI image, or a NixOS system closure deployed as a VM image. `flake.lock` is preserved. The Blob’s artifact store acts as a Nix binary-cache backend through a dedicated adapter.

`blobctl plan` evaluates and prints a stable diff. `blobctl apply --no-rebuild` trusts a digest already present in the artifact store. `blobctl dev` launches a development Machine from a flake `devShell` or `Dockerfile.dev` and forwards the terminal.

Import never applies foreign manifests directly. It translates them into canonical Blob manifests, reports unsupported features by name, shows a diff, and only then deploys. `blobctl export` produces portable manifests for migration off the platform; the export must round-trip back through `blobctl import`.

### 24. Managed services

| Service | Default implementation | Profile |
|---|---|---|
| Postgres | CloudNativePG with PITR; single Postgres-on-Machine for tiny solo | `core` / `full` |
| KV / cache | Valkey default; Dragonfly optional if license policy permits | `core` / `full` |
| Object bucket | SeaweedFS S3 in `core`; Ceph RGW optional in `full` | `core` / `full` |
| Event streams | NATS JetStream | `core` / `full` |
| ScyllaDB | ScyllaDB Operator | `full` only |
| MySQL/MariaDB | Percona XtraDB Operator | `full` |
| ClickHouse | ClickHouse Operator | `full` |
| Search | OpenSearch | `full` |
| Vector DB | Qdrant for `core`; Weaviate optional in `full` | `core` / `full` |
| Workflow engine | Temporal (community) | `full` |

`blob-sdk` ships for Node, Python, Go, and Rust. It reads injected service metadata, exposes typed clients, emits OpenTelemetry traces, and uses fair backoff. Raw connection strings still work for existing libraries.

Dragonfly and Redpanda are not defaults because both use Business Source License terms. They can be imported, used where policy allows, or offered as optional performance tiers after legal approval.

Every managed service has: backups, point-in-time recovery (where applicable), credentials injected as secrets, dashboards, alerts, snapshot/backup/rewind APIs (where applicable), per-team grants, and cost records.

### 25. Compute fairness and autoscaling

The Blob rejects fixed plans by default. A Machine can use idle CPU and memory unless the owner sets ceilings. Guards prevent harm.

- **CPU** — weights and optional caps.
- **Memory** — per-Machine ceilings and OOM targeting.
- **Disk I/O** — Volume fair-share floors and optional ceilings.
- **Network** — per-Machine egress shaping.
- **Pids, file descriptors, inotify watches, inodes** — generous defaults with hard ceilings.

The node baseline reserves resources for kubelet, containerd, storage agents, network agents, and control-plane services. A hostile Machine must be able to kill itself, not the node.

Autoscaling is a first-class manifest field. Modes:

- `off` — fixed `replicas`.
- `cpu` — scale on CPU utilization with a target.
- `memory` — scale on memory utilization with a target.
- `rps` — scale on HTTP requests-per-second observed by the L7 proxy.
- `concurrency` — scale on in-flight requests per replica (proxy-observed).
- `queue` — scale on queue depth in NATS / Postgres / Redis-protocol consumer.
- `external` — scale on a user-supplied OpenTelemetry metric.
- `schedule` — scale on cron windows (e.g. business hours).
- `composite` — combine modes with declared priority.

Scale-to-zero is allowed for HTTP services with idle thresholds; cold start uses warm blebs; the proxy holds the request until a replica is ready or until a profile-defined deadline. Stateful workloads can scale read replicas; primary-instance scale-to-zero is forbidden in `team` and `enterprise` profiles.

### 26. Deploy-speed budgets

| Scenario | Budget |
|---|---|
| Warm redeploy small service | p50 ≤ 5 s, p95 ≤ 10 s |
| Cold first deploy small service | p50 ≤ 30 s, p95 ≤ 60 s |
| Rollback | p50 ≤ 3 s |
| One-shot job dispatch | ≤ 2 s to running |
| Multi-component app, one component changed | p50 ≤ 7 s, p95 ≤ 15 s |
| Browser Machine cold start | p50 ≤ 15 s |
| Plain VM first boot | p50 ≤ 30 s |
| Provider-provisioned node joins fleet | p50 ≤ 90 s after provider says reachable |
| Preview environment per PR | p50 ≤ 45 s, p95 ≤ 90 s |
| Region failover cutover | p50 ≤ 3 min, p95 ≤ 5 min |
| Backup restore drill (small managed service) | p50 ≤ 5 min, p95 ≤ 10 min |
| Autoscaler cold-start replica (warm bleb) | p95 ≤ 1.5 s |

Every deploy prints phase timings: inspect, build, push, authz, placement, pull / lazy mount, boot, readiness, route. CI tracks canaries and fails on rolling p95 regression.

### 27. Provider catalog and purchasing

`blobctl provision plan` takes CPU, memory, disk, region, runtime class, architecture, GPU / confidential needs, max price, and provider allowlist. It returns ranked candidates from first-party provider snapshots and optional community-contributed snapshots.

`blobctl provision --auto` buys the selected instance, registers SSH/bootstrap identity, joins it as a worker, records cost, labels it. Bootstrap failure terminates the instance where the provider allows, rolls back the registry, and records an audit entry.

First-party provider clients for v1: Hetzner, OVHcloud, DigitalOcean, Linode/Akamai, Vultr, Scaleway, Latitude.sh, Equinix Metal, Fly Machines (as a worker source), and AWS / GCP / Azure on supported instance families. Additional providers can be community snapshot-only until a tested purchase client exists.

Spend caps are profile-driven. First use of a provider requires approval outside `solo`. Reserved-capacity contracts and savings-plan discounts are reflected in the cost engine.

### 28. Doctor and chaos

`blobctl doctor` reads manifests, projections, registry, audit, storage, network, image trust, gossip, deploy timings, observability backplane, billing, and managed services. It returns ranked issues with evidence and remediation.

Doctor checks at least: projection drift, unsigned deployed images, expired certs, cert quota risk, stale secret rotations, suspicious egress denies, exhausted public ports, gossip lag, drained nodes, hot-tier degradation, orphan snapshots, stale rewind logs, storage replica divergence, missing attestation, deploy-speed regression, grants overdue for review, alert firing without an owner, dashboards disconnected from running workloads, status-page entries with stale state, autoscaler thrash, queue lag exceeding budget, backup age exceeding policy, and budget burn rate.

Chaos runs only against opted-in targets. Scenarios include kill worker, drop hot tier, partition gossip, exhaust port pool, saturate egress, OOM Machine, slow backend, expire cert, revoke image signature, fail managed-service primary, and trigger a region failover. Each scenario asserts runbook recovery. Failures file issues. Chaos can be paused globally during incidents.

### 29. Observability

Observability is wired by default for every workload. A workload without dashboards and alerts is a deploy-time refusal, not a soft warning, on `team` and `enterprise` profiles.

- **Logs** — Vector ships container logs to Loki with workload labels (org, team, service, env, region, revision). The CLI streams logs with `blobctl logs` and the console shows a live tail. Long-term retention spills to object storage.
- **Metrics** — Prometheus-compatible scrape from each Machine and platform component, written to VictoriaMetrics. Default dashboards per workload include latency, error rate, throughput, saturation, restart count, and resource utilization.
- **Traces** — OpenTelemetry Collector ingests OTLP from workloads (SDK auto-injects instrumentation where possible) and platform components. Tempo stores traces; the console links logs to traces via trace ID.
- **Error tracking** — GlitchTip is provisioned by default; SDKs auto-configure DSNs from injected service metadata.
- **Alerts** — Alertmanager rules generated from the manifest plus platform defaults (latency SLO, error budget burn, restart loops, resource saturation, cert expiry, secret rotation overdue, backup age, queue lag).
- **SLOs** — Each `MachineService` and `App` may declare SLO targets (availability, latency, freshness). The platform computes burn rates and exposes them in the console; budget burns trigger alerts and optionally pause non-critical scale-out.
- **On-call routing** — Owner and backup owner are the default route; teams can integrate PagerDuty, Opsgenie, or signed-webhook destinations.

### 30. Backups and disaster recovery

Backups are a first-class platform capability, not a workload concern.

- **Volumes** — Snapshot schedule is platform-default unless overridden; off-site shipping via rclone to object storage (customer-owned destination on `enterprise`); snapshot retention is profile-driven; restore is a single CLI command and is exercised in scheduled drills.
- **Managed services** — Postgres uses CloudNativePG PITR; ScyllaDB uses operator-driven snapshots; object buckets use bucket replication; event streams snapshot consumer state.
- **Cluster state** — Velero backs up registry, secrets metadata (not values), audit anchor, and projection state.
- **Secrets** — Native store ships sealed backups encrypted under the cluster KEK; restoration requires unseal quorum.
- **Disaster recovery plan** — Each org has a documented DR plan generated from manifests: which workloads are critical, where backups live, RTO/RPO targets, the failover region, and the cutover procedure. `blobctl dr drill` runs the plan against a staging environment and emits a signed evidence file.

Profile-driven defaults:

| Profile | Backup retention | Off-site | Restore drill cadence |
|---|---|---|---|
| `solo` | 7 days | Optional | None |
| `core` | 30 days | Optional | Quarterly recommended |
| `team` | 90 days | Required | Monthly required |
| `enterprise` | Customer-defined | Customer destination required | Monthly required |

### 31. Multi-region and failover

v1 supports active-passive multi-region deploys. Active-active for application data is out of scope.

- **Topology** — A `home` region runs the workload; one or more `standby` regions replicate Volumes, secrets, certs, and registry state.
- **Replication** — Block Volumes use Longhorn cross-region replication on `full`; Postgres uses CloudNativePG streaming replication; secrets replicate over the Raft store; object buckets use bucket replication.
- **Failover** — `blobctl failover --to <region> --reason <text>` triggers a coordinated cutover: standby promotes, DNS / proxy / L4 forwarder reroutes, audit records the action, and an evidence file is produced.
- **Failback** — A controlled re-replication and reverse-cutover after the original region recovers.
- **Drills** — Quarterly on `team`, monthly on `enterprise`. A failed drill blocks production deploys until resolved.

### 32. CI/CD and preview environments

- **Auth** — Deploy tokens for simple cases, CI OIDC federation (GitHub Actions, GitLab CI, Buildkite, CircleCI, generic OIDC) for production, signed webhooks for receivers.
- **GitOps mode** — Manifests in a Git repo are reconciled by The Blob; pull-based reconciliation with signed commits.
- **Preview environments** — A PR webhook creates a fresh `Environment` with isolated secrets, scoped data (anonymized snapshot or shared read-only), a temporary subdomain, dashboards, and a TTL. Comments on the PR include the URL and a link to logs/metrics. Closing the PR destroys the environment and audits the cleanup.
- **Promotion** — A deploy in `staging` can be promoted to `prod` with `blobctl promote`, which rebuilds projections under the target environment’s policy without rebuilding the artifact.
- **Canary and blue/green** — Built-in traffic-splitting through the L7 proxy with health-gated promotion and automatic rollback on SLO breach.

### 33. Web console

The web console is a first-class client. It does everything the CLI does and shares auth, audit, and validation. Key surfaces:

- **Catalog** — every workload, app, managed service, and environment with owner, lifecycle, support tier, status, and last deploy.
- **Workload detail** — manifest viewer (with diffs), deploys timeline, rollback button, logs/metrics/traces, alerts, status-page entry, secrets (refs only), backups, and runbook.
- **Deploys** — phase timings, build/push/route timeline, recent revisions, rollback.
- **Observability** — embedded dashboards, ad-hoc log/trace search, SLO burn views.
- **Identity** — users, teams, service identities, grants, access reviews, audit search with hash-chain verification.
- **Cost** — rollups by workload/team/org/region, budgets, alerts, exports.
- **Doctor** — ranked issues, evidence, remediation, history.
- **Plugins** — installed plugins, versions, signatures, scopes, audit.

The console is generated from the same OpenAPI surface the CLI uses; component generators ensure surfaces cannot diverge. Browser sessions enforce profile-defined MFA conditions.

### 34. Cost management and billing

The Blob measures cost from the bottom up, not from a vendor invoice.

- **Inputs** — provider-instance prices, on-prem amortization (configured per-node), storage cost per Volume class, network egress cost per provider, reserved-capacity contracts.
- **Allocation** — every resource carries `org`, `team`, `service`, `env`, `region`, `lifecycle`. Cost is allocated by actual usage (CPU-seconds, RAM-GiB-seconds, IOPS, GB-month, egress GB).
- **Outputs** — per-workload, per-team, per-org, per-environment, per-region rollups; budgets with thresholds; alerts on burn rate; CSV/Parquet exports for downstream finance tools; showback and chargeback templates.
- **Enforcement** — budget breach can block non-critical scale-out, refuse new preview environments, or page the team owner. Deletion of cost records requires explicit approval and is audited.

### 35. GPU and AI workloads

GPU and AI workloads are first-class.

- **Hardware** — NVIDIA via the device plugin; AMD ROCm where supported; MIG slicing for A100/H100; per-GPU and per-MIG allocation.
- **Runtime** — `gpu` runtime class with image compatibility checks; CUDA version gates declared in the manifest.
- **Model server** — built-in `ManagedService` driver for vLLM and Triton; model artifacts shipped through the artifact store with content addressing; KV-cache hints propagated through the resource graph.
- **Inference autoscaling** — token-throughput and queue-depth scalers; warm-pool of model-loaded blebs to absorb spikes.
- **Confidential** — SEV-SNP and TDX runtime classes via Confidential Containers; attestation records stored in audit.

The platform refuses GPU asks on non-GPU fleets with a precise error. It refuses confidential placements on incapable nodes and produces remediation guidance.

### 36. Plugin and extension model

The Blob is extensible through signed plugins. Plugin kinds:

- **Runtime** — adds a runtime class (e.g. Firecracker, gVisor, Wasm).
- **Provider** — adds a node-purchasing client.
- **Secret backend** — adds a `secret://` resolver target.
- **Observability sink** — adds a logs/metrics/traces destination (e.g. Datadog, Honeycomb, S3 export).
- **Importer** — adds a foreign-format importer.
- **Managed-service driver** — adds a managed-service implementation.
- **Webhook receiver** — adds a signed-webhook handler with scoped permissions.

Plugins ship as OCI artifacts, are signed by the publisher, and are installed via `blobctl plugin install`. The control plane verifies signatures, scope, and required capabilities before activation. Plugins run in their own service identity with grants scoped to their declared needs. Doctor reports plugin health and signature freshness.

### 37. Migration tools

Importers in v1: Compose, Kubernetes manifests, Helm charts, Fly.io `fly.toml`, Procfile, Heroku `app.json`, Render `render.yaml`, Vercel/Netlify project config, Cloudflare Workers, Nix flakes, and direct Dockerfile detection. Each importer translates to canonical Blob manifests, lists unsupported features, and refuses to deploy until the user accepts the diff.

Exporters in v1: portable Blob manifest archive, Compose (best-effort), Kubernetes manifests (best-effort, labeled lossy where appropriate). The export path must round-trip back through `blobctl import` for the canonical archive.

A managed migration runbook exists for moving from Heroku, Render, Fly, and a stock Kubernetes deploy. Each runbook is tested in CI against a fixture project.

### 38. Status pages and uptime

Every workload and environment can have a status page. v1 ships built-in status-page rendering tied to doctor and on-call.

- **Sources** — synthetic external probes from regions outside the home region; SLO burn from observability; doctor incidents; manual incidents from `blobctl status incident`.
- **Surfaces** — public URL per workload (opt-in on `core`, default-internal on `team` and `enterprise`), embeddable widget, RSS/Atom feed, signed-webhook on state changes.
- **Lifecycle** — incident open / update / resolve from CLI, console, or agent; automatic incident creation when SLO burn or doctor severity exceeds thresholds; post-mortem template generated automatically on resolution.

### 39. Compliance and data residency

The platform documents and enforces:

- **Residency** — workloads can declare `region` constraints; placement refuses to schedule outside those regions; backups inherit residency.
- **Retention** — audit, logs, backups, and rewind logs each have profile-default retention with documented overrides.
- **Attestation** — confidential workloads produce attestation evidence stored in audit; failure to attest blocks deploy.
- **Signed publisher policy** — `enterprise` requires a non-empty publisher list; image pulls verify cosign signatures against that list.
- **Data classification** — workloads declare classification (`public`, `internal`, `confidential`, `restricted`); egress, exposure, and managed-service eligibility derive from it.
- **Right-to-erasure helpers** — managed services expose tenant-scoped delete primitives that propagate to backups within retention windows; the operation is logged and signed.

The compliance posture is published as a single document derived from the live registry, so audits do not depend on hand-written claims.

---

## Part III — Implementation, acceptance, and governance

### 40. Build plan

v1 is a single release. Internally the build proceeds in tracks that ship together. A track is releasable on its own only for internal testing; v1 ships when every track passes its gate and the integrated acceptance criteria pass.

#### Track A — Contract and registry

Schemas, manifest examples, projection hash rules, runbook template, owner guide, operator guide, profile defaults, action vocabulary, validation fixtures.
**Gate.** `make validate-contract` passes; every example has a projection hash; invalid fixtures fail at the expected path.

#### Track B — Control plane, identity, audit, RBAC

API, org/team/user/service identity model, WebAuthn, OIDC, deploy tokens, CI OIDC, grants, approvals, tamper-evident audit, profiles, invites, access query, registry records.
**Gate.** RBAC p99 under 1 ms at 10,000 grants; offboarding works; audit verify detects tampering; CI deploy auth paths work.

#### Track C — Fleet bootstrap and provider purchasing

Master bootstrap, worker join, node labels, laptop attach, provider catalog, provision plan/buy/join, drain/remove, cost records, fleet status.
**Gate.** Pi 4 4 GiB `core`-profile bootstrap; heterogeneous Pi + VPS + laptop fleet; two provider live smoke tests terminate cleanly.

#### Track D — Clients, packaging, artifact store, importers, exporters

`blobctl`, `/blob`, web console, project inspector, all v1 importers, exporters, BuildKit / Nix builders, zot registry, ORAS artifact storage, lazy-pull image format, signature preflight, SDKs.
**Gate.** Same fixture deployed by skill, CLI, and console produces identical registry state; two teams pushing the same base image dedupe; unchanged Nix flake skips build; round-trip export/import passes for every importer.

#### Track E — Machine runtime, deploy speed, autoscaling

Runtime adapter, Kata RuntimeClass, blebs, xDS routes, logs / metrics / traces wiring, restart, rollback, jobs, KubeVirt VM mode, browser mode, GPU runtime, autoscaling modes, scale-to-zero, canary, blue/green.
**Gate.** All deploy-speed canaries meet budgets; autoscaler cold-start meets ≤ 1.5 s p95 from warm bleb; canary rollback on SLO breach is automatic; container-only opt-in is labeled and auditable.

#### Track F — Storage fabric

Longhorn block, SeaweedFS object/file, Volume API, scheduler locality, hot journal behind a feature gate, snapshots, backups, encryption, tenant isolation tests, rewind opt-in, performance refusal rules, cross-region replication on `full`.
**Gate.** Cross-tenant read/write fails for block/file/object; acknowledged writes survive journal crash cases; hybrid performance below threshold passes; above-threshold requests are refused; backup restore drills pass.

#### Track G — Secrets, networking, domains, security

Native secret store, external secret drivers, secret CSI, egress enforcement, public IP/port pool, XDP/nftables L4, L7 proxy, ACME, DNS providers, Cloudflare Tunnel, confidential workload class, per-class minimal kernels, anycast/BGP integration on `full`.
**Gate.** Public HTTP and TCP/UDP work; bindings survive migration; domain modes work; denied egress is audited; confidential workload refuses non-confidential nodes; multi-region failover drill passes.

#### Track H — Managed services, apps, SDKs

CloudNativePG, Valkey, NATS JetStream, object buckets, ScyllaDB `full`-profile, MySQL/MariaDB, ClickHouse, OpenSearch, Qdrant, Temporal, SDKs, app dependency wiring, managed-service import conversions, app rollback.
**Gate.** Fixture app with Postgres + Valkey + bucket + event stream + vector DB deploys; SDK conformance passes in Node / Python / Go / Rust; Dragonfly / Redpanda remain optional under license policy; managed-service backup/restore drills pass.

#### Track I — Observability, status pages, on-call

Vector + Loki, Prometheus + VictoriaMetrics, OTel Collector + Tempo, GlitchTip, Alertmanager rule generation, SLO engine, on-call routing, status-page rendering and lifecycle, synthetic external probes.
**Gate.** Every deployed workload has dashboards, alerts, traces, and a status-page entry by default; SLO burn alerts fire correctly in fixtures; status-page state updates within 30 s.

#### Track J — CI/CD and preview environments

Deploy tokens, CI OIDC federation, signed webhooks, GitOps mode, per-PR preview environments with isolated secrets and scoped data, promotion, canary, blue/green.
**Gate.** Preview environment up ≤ 90 s p95 from CI webhook; PR close destroys environment and audits cleanup; promotion preserves manifest hash; canary rollback on SLO breach is automatic.

#### Track K — Cost, plugins, compliance

Cost engine, budgets, alerts, exports; plugin host with signature verification; residency, retention, attestation, signed-publisher enforcement; compliance posture document generation.
**Gate.** Cost rollups match synthetic ground truth within 1 %; budget breach blocks non-critical scale-out and audits the action; signed plugin install / uninstall paths work; residency violations are refused at preflight.

#### Track L — Doctor, chaos, pilots, handoff

Doctor scanners across all subsystems, chaos schedule, lockdown, access reviews, pilot workloads for every supported shape, handoff drills, DR drill workflow.
**Gate.** Non-builder operator completes each pilot drill using generated artifacts only; doctor is the canonical issue view after chaos; DR drill produces signed evidence.

### 41. Acceptance criteria

The release ships only if all of the following pass in CI or in a documented hardware lab.

#### Product and UX

- `/blob`, `blobctl`, and the web console deploy the same fixture and produce identical registry state.
- A fresh `solo` user logs in, deploys a Dockerfile service, and reaches a working platform-base HTTPS URL within 60 s of cluster availability.
- The skill asks only for fields not detected from the folder or org defaults.
- Every deployed workload has owner, backup owner, team, lifecycle, support tier, runbook, exposure, dashboards, alerts, status-page entry, backup schedule, cost record, audit entries, and projection hash.

#### Identity and compliance

- WebAuthn, OIDC, CI OIDC, deploy-token, signed webhook, and invite flows work.
- Offboarding kills sessions and keys, flags personal secrets, transfers service ownership without stopping service identities.
- `blobctl access query` returns deterministic answers across secrets, workloads, Volumes, domains, managed services, plugins.
- Audit hash-chain verification detects any modified entry.
- Profile floors cannot be loosened below hardcoded minimums.
- Residency, retention, attestation, and signed-publisher policy are enforced at preflight and documented.

#### Runtime and performance

- Persistent service, one-shot job, cron job, app, Bundle, plain VM, browser agent, GPU workload (when GPU exists), and confidential workload (when hardware exists) all deploy or refuse with a precise error.
- Warm redeploy p50 ≤ 5 s and p95 ≤ 10 s on canary.
- Rollback p50 ≤ 3 s.
- Autoscaler cold-start p95 ≤ 1.5 s on warm bleb.
- Slow deploys produce per-phase timing evidence.
- Canary rollback on SLO breach is automatic.

#### Storage and data

- Block, file, and object Volumes create, attach, snapshot, restore, backup, and delete through one API.
- Machine + Volume colocate when capacity fits.
- Hybrid placement reports local fraction, hit rate, journal lag, backend flush lag.
- Raw disk inspection returns ciphertext only.
- Cross-tenant read/write/restore/rewind fails without grant.
- Hot journal crash tests prove acknowledged writes survive or fail before ack.
- Rewind is rejected on Volumes that did not opt in.
- Backup restore drills pass for every Volume class and managed service.

#### Multi-region and DR

- Active-passive deploy creates standby replicas of Volumes, secrets, certs, registry.
- `blobctl failover` cuts over within budgets and produces signed evidence.
- Failback succeeds with no data loss in the documented scenarios.
- DR drills run on schedule and a failed drill blocks production deploys.

#### Security and networking

- Unsigned/untrusted images are rejected unless an explicit profile-allowed exception exists.
- Secret values never appear in manifests, logs, registry records, or generated runbooks.
- Production-like workloads default to egress deny unless profile overrides.
- Denied egress is logged and queryable.
- Confidential workloads require SEV-SNP / TDX capable nodes and attestation records.
- Public HTTP gets a valid cert and reaches the Machine.
- Public TCP and UDP echo fixtures work from outside the cluster.
- IP/port bindings survive redeploy, rollback, and Machine migration.
- Platform-base, user-managed, and user-external domains verify and attach.
- Cloudflare Tunnel routes a workload on a NAT/laptop node.
- Cert issuance stays within quota protections.

#### Managed services and SDKs

- Managed Postgres, Valkey, object bucket, NATS JetStream, ScyllaDB (`full`), MySQL/MariaDB, ClickHouse, OpenSearch, Qdrant, and Temporal provision through manifests.
- Credentials are generated and injected as secrets.
- SDK conformance passes in Node, Python, Go, Rust.
- Managed services expose snapshot/backup/rewind where semantics apply.
- Cross-team grants are required and audited.

#### Observability, status, on-call

- Every workload has logs, metrics, traces, dashboards, alerts, and a status-page entry by default.
- SLO burn alerts fire correctly in fixtures.
- Status-page state updates within 30 s of an underlying state change.
- On-call routing reaches owner and backup owner; integrations to PagerDuty / Opsgenie / signed webhooks work.

#### CI/CD and previews

- Preview environments come up ≤ 90 s p95 from a CI webhook.
- Closing a PR destroys the preview environment and audits the cleanup.
- Promotion preserves the manifest hash across environments.
- GitOps mode reconciles signed Git state into the registry.

#### Cost and plugins

- Cost rollups match synthetic ground truth within 1 % across workload/team/org/environment/region.
- Budget breach blocks non-critical scale-out and audits the action.
- Signed plugin install/uninstall paths work; unsigned or out-of-scope plugins are refused.

#### Operability

- `blobctl doctor` returns no issues on a clean fleet and exact expected issues on broken fixtures.
- Chaos scenarios run against opted-in canaries and assert runbook recovery.
- A non-builder completes handoff for every pilot shape.
- The manifest archive can rebuild the registry and redeploy the fleet from scratch.

### 42. Risks and mitigations

**The platform surface is wide.** Mitigation: profile-gating and refusal paths. ScyllaDB, KubeVirt, Ceph, Cilium, multi-region, and chaos are disabled on small fleets. `/blob` must explain why a requested shape is refused instead of degrading silently.

**Hot journal becomes a storage product inside the product.** Mitigation: hard feature gate. Core storage works without it. Hot journal only graduates after crash, replay, and performance tests pass.

**Kubernetes leaks into the user model.** Mitigation: projection ownership. Users edit Blob manifests, never runtime objects. Import converts K8s into Blob, not K8s into more K8s.

**Licenses poison the open / self-hosted promise.** Mitigation: defaulting to permissive components — Valkey over Dragonfly, NATS over Redpanda, SeaweedFS / Longhorn over AGPL-heavy defaults. Source-available components are optional and policy-gated.

**Tiny hardware promises regress.** Mitigation: separate `ultralight`, `core`, `full` profiles with CI footprint tests. The Pi 4 4 GiB target is tested, not waved through.

**Peer image cache becomes a trust hole.** Mitigation: per-pull signature verification and disabling peer-pull for restricted / confidential workloads.

**User-external domains become support traps.** Mitigation: deterministic DNS instructions, polling, ACME delegation, and doctor checks that name the missing record.

**Observability stack overwhelms small fleets.** Mitigation: profile-gated retention and sampling defaults; spillover to object storage; refusal to enable full traces on `ultralight`.

**Multi-region failover is rehearsed only on paper.** Mitigation: scheduled DR drills with signed evidence; failed drills block production deploys.

**Plugins expand attack surface.** Mitigation: signed-publisher requirement, scoped grants, mandatory doctor health and signature checks, and audit on install/uninstall.

**Cost engine drifts from reality.** Mitigation: synthetic ground-truth tests, provider-invoice reconciliation tooling, and per-month variance reports.

### 43. Organizational parameters required before first install

These are deployment choices, not technical unknowns. They must be filled in before production.

- OIDC provider and group-to-team mapping.
- Platform-base domain(s) and DNS provider token location.
- Provider purchasing allowlist and spend caps.
- Backup fault domain: separate provider, dedicated storage box, or customer S3.
- Multi-region topology: home region, standby region(s), and the DR drill cadence.
- Which teams receive `team` profile by default.
- Which pilot workloads cover each shape.
- Whether Dragonfly or Redpanda are allowed anywhere after legal review.
- Confidential-compute providers approved for SEV-SNP / TDX workloads.
- On-call integrations: PagerDuty, Opsgenie, signed-webhook destinations.
- Cost-export destinations and chargeback policy.
- Signed-publisher list for `enterprise` image trust.

### 44. Glossary

- **The Blob** — the platform.
- **`blobctl`** — the CLI.
- **`/blob`** — the Claude Code skill.
- **`blob.yaml`** — the canonical manifest authoring file.
- **bleb** — a clean warm microVM cell in the deploy pool. Contains kernel, guest agent, rootfs metadata, and image cache hints; never tenant state.
- **Machine, Bundle, Volume, Domain, Secret, Managed Service, App, Job, CronJob, Environment, Plugin** — the user-facing primitives.
- **Resource graph** — the CRDT-backed graph of signed node deltas the placement engine evaluates.
- **Hot journal** — the local NVMe + peer journal in front of durable storage backends, with three ack modes (`local-single`, `local-plus-peer`, `backend-sync`).
- **Manifest projection hash** — the hash that ties every projected runtime artifact back to the manifest version that produced it; the basis for drift detection.
- **Profile** — `ultralight`, `core`, `full`, or `enterprise`. Sets defaults; never forks code paths.
- **Doctor** — `blobctl doctor`. Continuously verifies projection hashes and live health.
- **Status page** — the platform-rendered public or internal truth surface tied to doctor, SLOs, and on-call.

### 45. Research references

1. Kubernetes RuntimeClass: https://kubernetes.io/docs/concepts/containers/runtime-class/
2. Kata Containers and hypervisor support: https://katacontainers.io/ • https://github.com/kata-containers/kata-containers/blob/main/docs/hypervisors.md
3. Kata with Cloud Hypervisor: https://katacontainers.io/blog/kata-containers-with-cloud-hypervisor/
4. k3s requirements and resource profiling: https://docs.k3s.io/installation/requirements • https://docs.k3s.io/reference/resource-profiling
5. KubeVirt architecture: https://kubevirt.io/user-guide/architecture/
6. Cilium kube-proxy replacement and WireGuard: https://docs.cilium.io/en/stable/network/kubernetes/kubeproxy-free/ • https://docs.cilium.io/en/stable/security/network/encryption-wireguard/
7. Longhorn: https://longhorn.io/docs/latest/what-is-longhorn/ • https://longhorn.io/docs/latest/concepts/
8. Longhorn install / RWX: https://longhorn.io/docs/latest/deploy/install/ • https://documentation.suse.com/cloudnative/storage/1.11/en/volumes/rwx-volumes.html
9. Rook / Ceph: https://rook.io/docs/rook/latest-release/ • https://docs.ceph.com/en/reef/cephfs
10. SeaweedFS and license: https://github.com/seaweedfs/seaweedfs • https://github.com/seaweedfs/seaweedfs/blob/master/LICENSE
11. Sigstore cosign verification: https://docs.sigstore.dev/cosign/verifying/verify/
12. containerd stargz lazy-pull: https://github.com/containerd/stargz-snapshotter • https://github.com/containerd/stargz-snapshotter/blob/main/docs/estargz.md
13. Nydus image service: https://nydus.dev/
14. Spegel OCI mirror: https://github.com/spegel-org/spegel • https://spegel.dev/
15. OCI Distribution and ORAS: https://specs.opencontainers.org/distribution-spec/ • https://oras.land/docs/concepts/artifact/
16. zot registry: https://zotregistry.dev/ • https://www.cncf.io/projects/zot/
17. Cloudflare Tunnel: https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/
18. Let’s Encrypt DNS-01 and rate limits: https://letsencrypt.org/docs/challenge-types/ • https://letsencrypt.org/docs/rate-limits/
19. Confidential Containers and attestation: https://confidentialcontainers.org/docs/getting-started/installation/ • https://confidentialcontainers.org/docs/features/get-attestation/
20. AMD SEV and Intel TDX: https://www.amd.com/en/developer/sev.html • https://www.intel.com/content/www/us/en/developer/tools/trust-domain-extensions/overview.html
21. Nix flakes, dockerTools, binary cache, develop: https://nix.dev/concepts/flakes.html • https://nix.dev/tutorials/nixos/building-and-running-docker-images.html • https://nix.dev/guides/recipes/add-binary-cache.html • https://nix.dev/manual/nix/2.18/command-ref/new-cli/nix3-develop
22. CloudNativePG: https://cloudnative-pg.io/
23. ScyllaDB Operator: https://operator.docs.scylladb.com/
24. Valkey: https://valkey.io/ • https://github.com/valkey-io/valkey
25. DragonflyDB license and operator: https://www.dragonflydb.io/docs/about/license • https://www.dragonflydb.io/docs/getting-started/kubernetes-operator
26. NATS / JetStream and Redpanda licensing: https://nats.io/ • https://github.com/nats-io/jetstream • https://docs.redpanda.com/current/get-started/licensing/overview/
27. XDP and Katran: https://docs.ebpf.io/linux/program-type/BPF_PROG_TYPE_XDP/ • https://github.com/facebookincubator/katran
28. Vector, Loki, Tempo: https://vector.dev/ • https://grafana.com/oss/loki/ • https://grafana.com/oss/tempo/
29. VictoriaMetrics: https://victoriametrics.com/
30. OpenTelemetry Collector: https://opentelemetry.io/docs/collector/
31. GlitchTip: https://glitchtip.com/
32. KEDA: https://keda.sh/
33. Velero: https://velero.io/
34. Temporal: https://temporal.io/
35. OpenSearch: https://opensearch.org/
36. ClickHouse Operator: https://github.com/Altinity/clickhouse-operator
37. Qdrant: https://qdrant.tech/
38. vLLM and Triton: https://vllm.ai/ • https://developer.nvidia.com/triton-inference-server
