# Istruzioni di sviluppo

Leggere `README.md` per architettura, percorso studente e limiti operativi.

## Comandi

- `make test`: suite Go.
- `make verify`: pre-commit, moduli, vulnerabilità con triage esatta, coverage ≥90%, race.
- `make integration`: lifecycle reale minikube senza binario minikube nel PATH.
- `make build`: compila entrambi i comandi.
- `make images`: immagini locali `minikube-cloud-lab/{cloud,lab}:dev`.
- `go test -tags docker . -run TestComposeConfiguration -v`: configurazione Compose.

Le prove reali richiedono Docker attivo, almeno 6 CPU e 6 GiB disponibili.
Su macOS e Linux usare Compose con rete bridge dedicata e porte localhost;
non richiedere host networking o binari cloud/lab nativi agli studenti.

## Mappa dei pacchetti

| Percorso | Responsabilità |
| --- | --- |
| `cmd/cloud`, `cmd/lab` | Entry point e segnali |
| `internal/cloud` | Modello, snapshot persistente, operazioni asincrone serializzate |
| `internal/api` | API HTTP/JSON e client |
| `internal/cli` | CLI cloud e server HTTP/gRPC |
| `internal/externalgrpc` | Provider Cluster Autoscaler sullo stesso servizio cloud |
| `internal/infra` | Confine infrastruttura |
| `internal/infra/minikube` | Libreria minikube, home isolata, quote Docker Linux |
| `internal/infra/fake` | Backend per test del servizio |
| `internal/labcmd` | Applicazione manifest via API Kubernetes |
| `lab/manifests` | YAML visibili, incorporati nel binario lab |
| `proto` | Protocollo externalgrpc e bindings generati |
| `tools/vulncheck` | Gate JSON govulncheck con triage ID/modulo/versione |

## Vincoli

- Nessun `os/exec`, shell, comando minikube o kubectl nel codice Go non di test.
  Sono autorizzati i processi interni della libreria upstream minikube; Docker CLI
  e curl nell'immagine servono a quella dipendenza. Usare client-go per Kubernetes.
- Non usare gli helper minikube CLI `node.Provision/Start/Add/Delete` o
  `machine.StartHost`: hanno percorsi `os.Exit`. Comporre le primitive Go verificate
  in `bootstrap.go` e mantenere gli errori al confine Infra. Driver Docker e indirizzi
  automatici soltanto; il caso fatal del driver KIC con StaticIP deve restare escluso.
- `lab-small`: 2 CPU / 2048 MiB; `lab-medium`: 4 CPU / 8192 MiB.
- Creazione cluster installa YAKD dagli asset minikube via client-go, senza helper addon fatal; il rollout resta osservabile via Kubernetes.
- `source scripts/configure.sh` prepara soltanto directory/.env/comandi di sessione; non deve eseguire Docker o operazioni sul cluster.
- Configurazione Bash/zsh su Linux/macOS; socket Docker Desktop GID0, GNUstat su Linux/BSDstat su Mac.
- Compose usa rete condivisa con cloud .254, distinta dagli IP2..253 assegnati da minikube. Kubeconfig privato per cloud/lab, endpoint localhost pubblicato per kubectl host; gRPC non pubblicato.
- Un solo cluster per servizio, un tipo per cluster, control plane incluso nei
  limiti min/max e protetto dalla cancellazione. Non introdurre supporto multi-cluster.
- Requester: `student` per la CLI/API, `cluster-autoscaler` per externalgrpc,
  `cloud` per eventi di riconciliazione. Entrambi i trasporti usano lo stesso Service.
- Mutazioni infrastrutturali serializzate; persistire l'operazione prima di avviarla.
  Ctrl-C del client non cancella il lavoro server. Non avviare servizi concorrenti
  sulla stessa directory di stato.
- Kubeconfig con certificati incorporati; conservare gli altri contesti.
- I manifest visibili sono la fonte usata da `lab` tramite embed. Mantenere Kubernetes
  v1.35.x, CA v1.35.0 e protocollo della stessa minor.
- `proto/externalgrpc.proto` proviene dal tag upstream `cluster-autoscaler-1.35.0`,
  copiato dal riferimento. Non modificare i bindings a mano: `proto/generate.sh`
  verifica le versioni esatte di protoc e plugin prima della rigenerazione.
- Il progetto `~/Documents/minikube-cluster-autoscaler` è soltanto un riferimento
  da leggere: non modificarlo.
- `docs/superpowers/` e `.superpowers/` restano ignorati. Non committare spec, piani,
  log, credenziali, `.env` o report locali.
- Repository e package GHCR pubblici: clonazione HTTPS e pull anonimo per gli studenti.
  CI amd64; build multiarch tramite QEMU.
- TDD per funzionalità e bugfix; commit piccoli con prefisso convenzionale e trailer
  `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` previsto dal piano.
