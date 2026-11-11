# Esercizio — Cluster autoscaling

Sezione «Cluster autoscaling» di `Kubernetes_5.pptx`. Un esercizio alla volta, con cleanup
prima del successivo. Partire dalla **radice di minikube-cloud-lab** per
configurare l’accesso, poi entrare in **kubernetes/lecture03** per i manifest.
Le risorse Kubernetes si creano con `kubectl apply`; il carico si aumenta
e si riduce con `kubectl scale`. I comandi `cloud` servono per il cluster
e per osservare il provider.

I manifest dell’autoscaler sono copie dei file già presenti nel progetto,
conservate in [autoscaler/](autoscaler/). [load.yaml](load.yaml) deriva dal
workload originale, con la sola modifica di `replicas: 4` in `replicas: 0`.
Riferimento: [tutorial del progetto](../../docs/student-tutorial.md).

**Obiettivo:** collegare Pod non schedulabili, decisione del Cluster Autoscaler,
operazione cloud e disponibilità di un nuovo Node; poi osservare lo scale-down.
Gli studenti impostano le repliche del Deployment **0 → 4 → 0**; il controller
del Deployment le mantiene e il Cluster Autoscaler adegua i nodi.
La pressione è sulle **requests**,
non sul consumo CPU misurato, e non viene installato un HPA.

## 1. Preparazione

Completare il cleanup dell'esercizio precedente, compreso il load-generator.
Controllare le risorse prima di sostituire un cluster:

```sh
source scripts/configure.sh
docker compose up -d
cloud clusters list
kubectl config current-context
kubectl get deployments,statefulsets,pods,hpa,pvc -A
cloud volumes list
```

Se non esiste un cluster, la lettura Kubernetes può riferirsi a un vecchio
contesto: usare l'inventario cloud per decidere il setup. Il provider gestisce
un solo cluster. Eliminare `lecture03` **solo se dedicato, vuoto e sacrificabile**;
per cluster con dati o risorse altrui chiedere all'operatore.

```sh
# Solo per sostituire il precedente lecture03 vuoto:
cloud clusters delete lecture03

# Se non esiste già un cluster con questa configurazione:
cloud clusters create lecture03 --machine-type lab-small --min-nodes 1 --max-nodes 3
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes
cloud clusters describe lecture03
```

Checkpoint: un control plane Ready, tipo `lab-small`, MIN 1 / MAX 3.
Ogni nodo ha limite 2 CPU / 2 GiB: prevedere risorse sull'host per tre nodi
più i servizi del provider. `cloud clusters resize` non serve a configurare
l'autoscaling: imposta min e max allo stesso valore e non cambia il tipo macchina.

## 2. Creare le risorse e osservare

Dopo il setup, dal repository entrare nella cartella dell’esercizio:

```sh
cd kubernetes/lecture03
kubectl apply -f autoscaler/
kubectl -n kube-system rollout status deployment/cluster-autoscaler --timeout=5m
kubectl apply -f load.yaml
kubectl -n default get deployment lab-load
kubectl -n default get pods -l app=lab-load
```

Checkpoint: autoscaler disponibile e `lab-load` esistente con **zero repliche**
e nessun Pod. Leggere i manifest [Deployment](autoscaler/deployment.yaml),
[RBAC](autoscaler/rbac.yaml) e [cloud-config](autoscaler/cloud-config.yaml).
Il controller usa `--cloud-provider=externalgrpc` e l'endpoint interno
`host.minikube.internal:9090`. `kubectl apply -f autoscaler/` crea
ServiceAccount, RBAC, ConfigMap e Deployment del controller.

In due terminali aggiuntivi, dalla radice del progetto, eseguire
`source scripts/configure.sh` e avviare i watch **prima del carico**:

```sh
# Terminale B
kubectl -n default get pods -l app=lab-load -w
# Terminale C
kubectl get nodes -w
```

Ctrl+C termina solo l'osservazione. La lista inizialmente vuota dei Pod è attesa.

## 3. Scale-up

Leggere [load.yaml](load.yaml): il template di ciascun Pod richiede 600m CPU
e 64Mi. Passando da zero a quattro repliche si richiedono **2,4 CPU**.
Prevedere perché queste richieste più
le requests di sistema non entrano in un nodo da 2 CPU. I container `pause`
non bruciano deliberatamente CPU.

Nel terminale A, dalla cartella `kubernetes/lecture03`:

```sh
kubectl -n default scale deployment/lab-load --replicas=4
kubectl -n default get events --field-selector reason=FailedScheduling --sort-by=.lastTimestamp
kubectl -n default rollout status deployment/lab-load --timeout=5m
kubectl -n default get deployment lab-load
kubectl -n default get pods -l app=lab-load -o wide
kubectl get nodes
cloud machines list --cluster lecture03
cloud operations list
kubectl -n kube-system logs deployment/cluster-autoscaler --since=5m
```

Checkpoint: quattro repliche disponibili, almeno due nodi Ready e operazione
`ADD_MACHINE` con requester `cluster-autoscaler` e stato `DONE`.
Registrare Pending / `Insufficient cpu`, ID operazione, nodo creato,
`CREATED_BY` e distribuzione dei Pod. Per approfondire:
`cloud operations describe OPERATION_ID` sostituendo l'ID reale.

Un terzo nodo può essere richiesto durante l'assestamento della capacità:
è ammesso dal massimo di tre. Se scade il rollout mentre il provider sta ancora
provisionando, continuare a osservare fino a 15 minuti complessivi; in caso di
ERROR o mancata convergenza, salvare log/eventi e consultare l'operatore.
Non ridimensionare manualmente i nodi durante la dimostrazione. Non è installato
un HPA: lo scaling dei Pod è il comando impartito dagli studenti, lo scaling
dei nodi è automatico. Non riapplicare `load.yaml` durante la fase a quattro
repliche: il file dichiara zero e riporterebbe il Deployment a zero.

## 4. Scale-down e cleanup

Portare a zero le repliche senza eliminare ancora il Deployment.
Lasciare attivo il Cluster Autoscaler per completare la rimozione dei worker.

```sh
kubectl -n default scale deployment/lab-load --replicas=0
kubectl get nodes -w
# In un altro terminale, ripetere fino al checkpoint:
cloud machines list --cluster lecture03
cloud operations list
```

Checkpoint: solo il control plane Ready; tutte le operazioni `DELETE_MACHINE`
dei worker in stato `DONE`, richieste da `cluster-autoscaler`; nessuna operazione
RUNNING. La scomparsa del Node non prova che il teardown cloud sia concluso.
Il lab usa ritardi di un minuto, ma il ciclo richiede alcuni minuti e rimuove
un worker alla volta. Il minimo è uno e il control plane è protetto.

**Solo dopo quel checkpoint**, terminare i watch e disinstallare il controller:

```sh
kubectl delete -f load.yaml
kubectl delete -f autoscaler/ --cascade=foreground
# Oggetti generati dal controller durante questa prova:
kubectl -n kube-system delete --ignore-not-found \
  configmap/cluster-autoscaler-status lease/cluster-autoscaler
kubectl -n default get deployment,pods -l app=lab-load
kubectl -n kube-system get deployment cluster-autoscaler --ignore-not-found
kubectl get nodes
```

Attesi: nessuna risorsa lab-load, nessun Deployment dell’autoscaler e un nodo Ready. Lasciare il cluster acceso e pulito.
Non disinstallare l’autoscaler prima del ritorno al minimo. La cancellazione
foreground attende anche la rimozione del Pod del controller; solo dopo
rimuovere ConfigMap di stato e Lease generati. Questi due oggetti non sono
nel manifest di installazione e resterebbero dopo il solo `delete -f`.

## 5. Passaggio agli altri esercizi

Per il nuovo [esercizio KEDA](keda/README.md), subito dopo CA nel deck,
riutilizzare il nodo lab-small pulito e lasciare Cluster Autoscaler disinstallato.
Completare anche il cleanup KEDA prima di proseguire.

Il cluster lab-small serve a questa dimostrazione. Per MySQL utilizzare
il setup già verificato a **due lab-medium**, descritto in [README.md](README.md).
Dopo il cleanup completo e solo sul cluster dedicato vuoto:

```sh
cloud clusters delete lecture03
cloud clusters create lecture03 --machine-type lab-medium --min-nodes 2 --max-nodes 2
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
```

Questi comandi di ripristino non sono stati eseguiti al termine della prova CA:
l'operatore ha chiesto di lasciare il cluster pulito al minimo di un nodo.

## Prove e discussione

La procedura qui descritta è stata ripetuta l’11 novembre 2026 alle
12:39–12:46 UTC **usando solo i manifest locali, `kubectl apply` e
`kubectl scale`**: Deployment 0 → 4 → 0, nodi Ready 1 → 3 → 1.
Le due aggiunte e le due rimozioni del provider sono terminate in `DONE`.
Il cleanup ha rimosso anche ConfigMap di stato e Lease generati dal controller.
Nessun timeout API o errore operativo osservato in questa ripetizione.
Dettagli: [SESSION.md](SESSION.md#ripetizione-cluster-autoscaling-con-kubectl--11-novembre-2026).

Nella prima prova dell’11 novembre 2026, effettuata con gli helper del progetto:
un nodo iniziale, tre Pod Pending per CPU insufficiente,
due worker aggiunti automaticamente, quattro repliche disponibili; dopo lo
stop, entrambi i worker rimossi automaticamente e ritorno a un nodo. Nessun
errore operativo o timeout API osservato in questa prova. Dettagli e log in
[SESSION.md](SESSION.md#esercizio-aggiunto--cluster-autoscaling).

- Chi sceglie quattro repliche? Lo studente con `kubectl scale`.
- Chi le mantiene? Il Deployment controller.
- Chi assegna i Pod ai nodi? Lo scheduler.
- Chi richiede capacità al provider? Il Cluster Autoscaler.
- Chi crea e rimuove le macchine? Il provider minikube-cloud-lab.
- Perché CPU misurata bassa e Pod Pending possono coesistere? Lo scheduler
  valuta le requests, già impegnate dagli altri Pod.

La variante opzionale con dieci repliche del tutorial non è inclusa né provata.
Riferimento concettuale: [Kubernetes — Node Autoscaling](https://kubernetes.io/docs/concepts/cluster-administration/node-autoscaling/).
