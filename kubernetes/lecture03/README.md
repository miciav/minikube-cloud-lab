# Kubernetes_5 — lecture03

Esperimenti di autoscaling e Operator pattern da eseguire con minikube-cloud-lab.
La procedura di accesso al provider riprende `Kubernetes_4.pptx`.
Per HPA, VPA, CRD e MySQL è stata verificata con l'operatore una configurazione dedicata:
**due nodi `lab-medium`, un control plane e un worker**, con i workload in
`default`. Eseguire un esercizio alla volta e rimuoverne gli oggetti prima
che inizi il successivo.

**Verificato l’11 novembre 2026:** HPA 1 → 6 → 1, CRD creata e rimossa,
MySQL con tre membri ONLINE e connessione SQL riuscita; successivamente
Cluster Autoscaler 1 → 3 → 1 nodi e KEDA 0 → 2 → 0 repliche. Cleanup completo
tra gli esercizi; risultati e limiti in [SESSION.md](SESSION.md).

Il nuovo esercizio **Cluster Autoscaler**, usa invece un cluster
`lab-small` con minimo 1 / massimo 3 nodi. Seguire
[CLUSTER_AUTOSCALING.md](CLUSTER_AUTOSCALING.md): i manifest sono in `lecture03/autoscaler/` e `lecture03/load.yaml`.
Gli studenti creano le risorse con `kubectl apply` e impostano le repliche
0 → 4 → 0 con `kubectl scale`; i nodi vengono scalati automaticamente.
**Stato della prova manuale CA:** un nodo lab-small Ready, workload e
autoscaler rimossi; intervallo cloud 1–3. Prima di MySQL ripristinare il setup
lab-medium come indicato nella guida, dopo il cleanup.
**Stato attuale dopo la verifica automatica:** cluster e volumi eliminati,
come richiesto dall’operatore; dettagli nella sezione finale di [SESSION.md](SESSION.md).

Subito dopo Cluster Autoscaler, l’esercizio [KEDA Cron scaler](keda/README.md)
riusa il nodo pulito per una finestra temporale con **0 → 2 → 0 repliche**.
Include i due manifest didattici e la release ufficiale KEDA 2.21.0 importata
in `keda/upstream/`. Aggiornare gli orari UTC prima di ogni esecuzione.

## Setup HPA / VPA / CRD / MySQL

Dalla radice del progetto, in ogni terminale:

```sh
source scripts/configure.sh
docker compose up -d
cloud clusters list
cloud machine-types list
```

Se non esiste un cluster, creare il laboratorio dimensionato per questi esperimenti:

```sh
cloud clusters create lecture03 --machine-type lab-medium --min-nodes 2 --max-nodes 2
cloud clusters get-credentials lecture03
kubectl wait --for=condition=Ready nodes --all --timeout=5m
kubectl get nodes -o wide
cloud machines list --cluster lecture03
```

Se esiste già un cluster del corso adatto, riutilizzarlo sostituendo il nome.
Il provider gestisce un solo cluster: non eliminare dati o cluster esistenti
per eseguire questa lezione. Non è possibile cambiare il tipo macchina con
`resize`: quel comando modifica il numero di nodi.

Ogni `lab-medium` ha un limite di 4 CPU / 8 GiB; i due nodi arrivano quindi
a 8 CPU / 16 GiB complessivi, oltre ai servizi di supporto. I limiti non
prenotano memoria fisica: verificare capacità e consumo reale con
`docker stats --no-stream`, soprattutto su un host da circa 16 GiB.
Due nodi sullo stesso host non forniscono tolleranza al guasto dell'host.

Le slide 4 partono invece da un `lab-small` e lo espandono a tre nodi.
Nella prima prova, tre `lab-small` con HPA e MySQL sovrapposti hanno raggiunto
il limite di memoria del control plane da 2 GiB, con timeout dell'API.
La scelta dei due nodi più capienti e l'esecuzione sequenziale sono
adattamenti autorizzati per questa lezione; i dettagli sono in `SESSION.md`.
Non è stata verificata la variante MySQL isolata su tre `lab-small`.

```sh
kubectl config current-context
kubectl config view --minify -o 'jsonpath={..namespace}'
kubectl get pods,deployments,statefulsets,services,hpa,pvc -A
kubectl get crd
kubectl get storageclass
cloud storage status
cd kubernetes/lecture03
```

Verificare contesto, namespace `default` (anche un valore vuoto lo indica),
capacità e assenza di collisioni con i nomi degli esercizi. Non applicare
l'intera cartella con `kubectl apply -f .`: CRD e operatori hanno un ordine
di installazione. `commands.txt` è una traccia manuale con checkpoint,
non uno script da eseguire senza supervisione.

## Esercizi

| Esercizio | File | Obiettivo e criterio di riuscita |
| --- | --- | --- |
| HPA | `php-apache.yml`, `hpa.yml` | Aumento delle repliche sotto carico e ritorno a 1 |
| VPA | `vpa/` | Off: solo raccomandazione; Recreate: nuovi Pod con request CPU maggiori, due repliche |
| Cluster Autoscaler | `CLUSTER_AUTOSCALING.md`, `autoscaler/`, `load.yaml` | Pending per CPU, aggiunta nodi, quattro repliche disponibili e ritorno a un nodo |
| KEDA Cron scaler | `keda/` | Finestra UTC, repliche 0 → 2 → 0; HPA creato da KEDA |
| CRD | `crontab-crd.yml`, `crontab-example.yml` | Registrare un tipo e creare/leggere/eliminare un oggetto |
| MySQL Operator | `mysql-operator-26.10.0-2.3.1/`, `mycluster.yml` | Tre membri ONLINE e connessione SQL attraverso Router |

VPA segue HPA; Cluster Autoscaler e KEDA precedono le estensioni Kubernetes.
Le slide nascoste su PostgreSQL non sono incluse. I riferimenti usano i titoli
per rimanere validi quando il docente inserisce altre slide.

### VPA

Seguire [vpa/README.md](vpa/README.md). L'esercizio riusa i due lab-medium dopo
il cleanup HPA. VPA 1.7.1 osserva due Pod che consumano CPU: prima raccomanda
in modalità `Off`, poi li sostituisce in `Recreate`. Controlla soltanto le
request CPU, mantenendo limiti, memoria, repliche e nodi.
**Prova riuscita:** request 50m → 500m su entrambi i nuovi Pod; cleanup completo
e cluster eliminato. Manifest upstream e generatore TLS sono inclusi.

### HPA

Il provider installa già Metrics Server: verificare Deployment, API e
`kubectl top nodes`. Non eseguire `minikube addons enable metrics-server`
sul profilo personale dell'host. Il carico HTTP delle slide viene generato
da `busybox:1.28` e interrotto con Ctrl+C nel terminale che lo ha avviato.

La CPU target è il 50% della richiesta di 200m, cioè circa 100m per Pod;
il limite di 500m non è il denominatore. Le repliche consentite vanno da
1 a 10. I valori iniziali `<unknown>` possono precedere la prima raccolta
delle metriche. Attendere `ScalingActive=True` prima di avviare il carico.

Lo scale-down non è necessariamente immediato: la finestra predefinita di
stabilizzazione è di 300 secondi. Attendere il ritorno a una replica senza
modificare manualmente il Deployment o accorciare la finestra.
Vedere il [funzionamento HPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/).

### CRD

La CRD aggiunge `CronTab` all'API; il custom object contiene `cronSpec`
e `image`. Non è il `CronJob` integrato di Kubernetes e non avvia processi:
in questo esercizio non è presente un controller che interpreti tali campi.
Prima si attende `Established`, poi si crea l'istanza. Eliminare la CRD
rimuove anche eventuali istanze: procedere solo se è stata creata per questa
prova e non contiene oggetti altrui.
Fonte: [esempio Kubernetes](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/).

### MySQL Operator

I manifest upstream sono conservati localmente al tag
`26.10.0-2.3.1`, aggiornato su richiesta dell’operatore; il relativo
default server/router è MySQL `26.10.0`.
Anche il client MySQL Shell usa quel tag, invece di `latest`.

L'operatore crea e riconcilia StatefulSet, Pod, Service e PVC a partire
dall'oggetto InnoDBCluster. Il manifest originale richiede tre istanze
MySQL e un Router. Con due nodi, due istanze possono condividere lo stesso
nodo: la replica del database non garantisce isolamento dai guasti di nodo.
Nel laboratorio vengono creati tre PVC da 2 GiB sulla
StorageClass predefinita `cloud-standard` (6 GiB complessivi).
Verificare questa classe e il budget prima di applicarlo.

La password `cloud` è il valore pubblico delle slide, destinato solo al
laboratorio isolato. È creata con `kubectl create secret`; non sono
inclusi kubeconfig o credenziali private. I certificati MySQL sono
autofirmati come nell'esempio originale.

Controllare il dominio reale in CoreDNS. Il tag dell'operatore rileva il
dominio automaticamente; non assumere che ogni versione del provider usi
lo stesso nome. Nella prova corrente CoreDNS usa `cluster.local`.
La sola esistenza dei Pod non basta: attendere stato ONLINE con tre membri,
poi verificare connessione e membership tramite le query in `verify-mysql.sql`.

Fonti: [installazione](https://dev.mysql.com/doc/mysql-operator/en/mysql-operator-installation-kubectl.html),
[InnoDB Cluster](https://dev.mysql.com/doc/mysql-operator/en/mysql-operator-innodbcluster-simple-kubectl.html),
[rilevamento DNS nel tag usato](https://github.com/mysql/mysql-operator/blob/26.10.0-2.3.1/mysqloperator/controller/kubeutils.py).

## Cleanup e risultati

Prima di iniziare un nuovo esercizio verificare che il precedente non abbia
lasciato Pod, Deployment, HPA, custom object o volumi.
Eliminare solo gli oggetti creati dalla prova. Rimuovere il custom object
MySQL mentre l'operatore è ancora attivo; verificare i PVC residui e i
volumi cloud prima di disinstallare l'operatore. Le istruzioni puntuali
sono in `commands.txt`. Non eliminare namespace `default`, volumi altrui
o il cluster del corso.

`SESSION.md` distingue risultati osservati, adattamenti e limiti.
I log locali sono in `evidence/`, esclusa da Git.
I manifest HPA/MySQL provengono dai materiali originali
`Cloud 2025 lectures - see Drive/Esercitazioni/K8S-Lecture04`;
`SHA256SUMS` registra i manifest consegnati.


## Esecuzione automatica del deck

Dalla radice del repository, con i servizi cloud già avviati:

```sh
source scripts/configure.sh
labcompose up -d
python3 scripts/verify-lecture03.py --recreate-empty-cluster
# Per scegliere esplicitamente il runtime: aggiungere --runtime docker oppure --runtime podman.
```

È un secondo script, distinto da `scripts/verify-lab.py`, e usa solo la libreria
standard Python. Richiede il laboratorio **dedicato e sacrificabile `lecture03`**:
`--recreate-empty-cluster` autorizza la cancellazione e ricreazione del cluster
vuoto tra i diversi dimensionamenti. Non eseguirlo sul cluster di altri studenti
o mentre qualcuno sta modificando il laboratorio. Il provider deve essere già
attivo; il test non arresta i servizi Compose.

Prima di modificare il cluster controlla checksum dei manifest, namespace,
workload/risorse in default, controller negli altri namespace, CRD, PV,
volumi cloud e installazioni autoscaler/adapter preesistenti. Se trova collisioni,
risorse estranee o operazioni cloud in corso, si ferma. Non elimina dati
preesistenti per preparare il test.

La sequenza segue il deck:

1. **HPA**, due lab-medium: da una a più repliche disponibili e ritorno a una,
   attendendo la stabilizzazione standard dopo la rimozione del generatore.
2. **VPA**, stessi due lab-medium: raccomandazione Off senza modifiche, poi
   Recreate con due nuovi UID e request CPU maggiori; memoria e limiti invariati.
3. **Cluster Autoscaler**, lab-small min 1 / max 3: apply dei file locali,
   scale 0 → 4 → 0, Pod Pending per CPU, almeno due nodi Ready, operazioni
   cloud ADD_MACHINE e DELETE_MACHINE della prova terminate in DONE.
4. **KEDA**, stesso nodo pulito: installazione, finestra UTC 0 → 2 → 0,
   HPA posseduto dallo ScaledObject e risposta HTTP `Hello, KEDA!`.
   Gli orari vengono aggiornati in una copia temporanea; il file nel repository
   e il suo checksum restano invariati, anche attraversando la mezzanotte UTC.
5. **CRD**, stesso nodo: registrazione di CronTab, verifica dello spec e cleanup.
6. **MySQL Operator**, due lab-medium: tre membri ONLINE, Router disponibile,
   query SQL attraverso Router e rilascio di PVC, PV e volumi cloud.

Ogni esercizio viene ripulito prima del successivo. Per InnoDBCluster si usa
la propagazione `background`: l’operatore deve poter leggere i propri Secret
fino al completamento dei finalizer. La propagazione `foreground` resta usata
per gli altri manifest, incluso il Deployment del Cluster Autoscaler.
Alla fine lo script **elimina il cluster lecture03** e verifica che il provider
non contenga cluster o volumi residui. I servizi cloud Compose restano attivi.
Prevedere 40–75 minuti, immagini scaricabili e risorse host per due nodi da
4 CPU / 8 GiB; i limiti sono per nodo, il consumo effettivo può essere inferiore.
`--timeout SECONDI` cambia il limite delle attese di convergenza (default 900);
le singole chiamate e i rollout hanno limiti propri.

Il percorso dei log è stampato all’avvio: `commands.log`, snapshot JSON,
manifest KEDA temporaneo, risultato SQL e `report.json` con PASS/FAIL.
Exit code 0 significa che tutte le verifiche e il cleanup sono riusciti;
1 indica errore/interruzione, 2 argomenti CLI non validi.
In caso di fallimento raccoglie diagnostica, tenta il cleanup dell’esercizio
attivo e **non passa al successivo né ricrea il cluster per nascondere residui**.
Consultare l’operatore con report e log; non forzare finalizer o cancellare volumi
per far passare una prova. SIGINT/SIGTERM durante un esercizio seguono il cleanup;
un’interruzione durante il provisioning può lasciare un’operazione cloud in corso.

Test senza Docker/Kubernetes, eseguiti anche dalla CI:

```sh
python3 -m unittest discover -s tests -p 'test_verify*.py'
```
