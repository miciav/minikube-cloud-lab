# Development instructions

The [README](README.md) lists the course materials. See the [development guide](docs/development.md) for architecture and tests and the [operations guide](docs/operations.md) for state and limits.

## Commands

- `make test` runs the Go suite.
- `make verify` checks pre-commit hooks, modules, vulnerabilities against exact triage entries, coverage (≥90%) and races.
- `make integration` exercises the real minikube lifecycle without a minikube executable on PATH.
- `make build` builds both commands.
- `make images` builds local `minikube-cloud-lab/{cloud,lab}:dev` images.
- `go test -tags docker . -run TestComposeConfiguration -v` checks the Compose configuration.

Live tests require Docker with at least 6 CPUs and 6 GiB available. On macOS and Linux, use Compose with a dedicated bridge and localhost ports. Students run the cloud and lab commands through Compose.

## Package map

| Path | Responsibility |
| --- | --- |
| `cmd/cloud`, `cmd/lab` | Entry points and signal handling |
| `internal/cloud` | Domain model, persistent snapshot and serialized asynchronous operations |
| `internal/api` | HTTP/JSON API and client |
| `internal/cli` | Cloud CLI and HTTP/gRPC server |
| `internal/externalgrpc` | Cluster Autoscaler provider on the cloud service |
| `internal/infra` | Infrastructure interface |
| `internal/infra/minikube` | Minikube library, isolated home and Linux Docker quotas |
| `internal/infra/fake` | Infrastructure backend for service tests |
| `internal/labcmd` | Manifest application through the Kubernetes API |
| `internal/atomicfile` | Durable replacement of state files, kubeconfig and NFS exports |
| `lab/manifests` | YAML sources embedded in the lab binary |
| `proto` | External gRPC protocol and generated bindings |
| `tools/vulncheck` | govulncheck JSON gate with triage by ID, module and version |

## Constraints

- Keep `os/exec`, shell, minikube and kubectl invocations out of production Go code. The upstream minikube library may start internal processes and requires the Docker CLI and curl in the image. Use client-go for Kubernetes.
- Avoid minikube CLI helpers `node.Provision/Start/Add/Delete` and `machine.StartHost`, which can call `os.Exit`. Compose the verified Go primitives in `bootstrap.go` and return errors at the Infra boundary. Use the Docker driver with automatic addresses to avoid the KIC driver's fatal StaticIP path.
- `lab-small` has 2 CPUs and 2048 MiB. `lab-medium` has 4 CPUs and 8192 MiB.
- Cluster creation installs YAKD from minikube assets through client-go. Avoid addon helpers with fatal paths. The rollout must be observable through Kubernetes.
- Sourcing `scripts/configure.sh` prepares directories, .env and session commands. It must leave Docker and the cluster untouched.
- Use one `scripts/configure.sh` for rootful Docker and rootless Podman in Bash and zsh on Linux and macOS. Account for Docker Desktop socket GID 0, GNU stat on Linux and BSD stat on macOS.
- Compose shares a network with minikube. Reserve the cloud address (.254) separately from node addresses. Use a private kubeconfig for cloud/lab and publish a localhost endpoint for host kubectl. Keep gRPC internal.
- Each service manages one cluster with one machine type. The control plane counts toward the minimum and maximum and is protected from deletion.
- Record the requester as `student` for CLI/API calls, `cluster-autoscaler` for externalgrpc and `cloud` for reconciliation events. Both transports share the same Service.
- Serialize infrastructure mutations and persist each operation before starting it. Client Ctrl-C leaves server work running. Only one service may use a state directory at a time.
- Embed certificates in kubeconfig and preserve other contexts.
- The visible manifests are the sources embedded by `lab`. Keep Kubernetes at v1.35.x, Cluster Autoscaler at v1.35.0 and the protocol on the same minor version.
- `proto/externalgrpc.proto` comes from upstream tag `cluster-autoscaler-1.35.0` via the reference project. Regenerate bindings with `proto/generate.sh`, which checks exact protoc and plugin versions first.
- Treat `~/Documents/minikube-cluster-autoscaler` as a read-only reference.
- Keep `docs/superpowers/` and `.superpowers/` ignored. Exclude local specifications, plans, logs, credentials, .env and reports from commits.
- Keep the repository and GHCR packages public for HTTPS cloning and anonymous image pulls. CI runs on amd64 and uses QEMU for multiarch builds.
- Use TDD for features and fixes. Make small commits with conventional prefixes and the trailer specified by the plan, `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
