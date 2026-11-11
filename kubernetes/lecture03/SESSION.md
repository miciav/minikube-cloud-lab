# Registro prove — 11 novembre 2026

**Esito: HPA, CRD, MySQL, Cluster Autoscaler e KEDA riusciti,
eseguiti separatamente con cleanup verificato.**
Le nuove prove CA e KEDA usano un nodo lab-small, con cleanup separato;
la precedente configurazione a due lab-medium è descritta come storico.
I log autentici sono in `evidence/` (esclusa da Git). Tutti gli orari sotto
sono UTC; in Italia aggiungere un'ora.

## Ambiente delle prove HPA / CRD / MySQL

- Cluster `lecture03`: due nodi `lab-medium`, un control plane e un worker.
- Ogni nodo: quota 4 CPU (`CPUQuota=400000`, `CPUPeriod=100000`), limite 8 GiB.
- Host Docker ARM64: 11 CPU, 16.745.623.552 byte di memoria (circa 15,6 GiB).
- Kubernetes server v1.35.6, kubectl del progetto v1.35.10.
- CoreDNS: `cluster.local`; Metrics Server già installato dal provider.
- StorageClass predefinita `cloud-standard`, NFS, reclaim policy Delete.
- MySQL Operator 9.1.0-2.2.2; server, Router e Shell MySQL 9.1.0.
- Immagini del provider e digest: [28-runtime-versions.log](evidence/28-runtime-versions.log).
  Quote e client del progetto: [29-client-and-quotas.log](evidence/29-client-and-quotas.log).

Il setup di Kubernetes_4 (slide 2–6) è stato letto ed eseguito inizialmente
con un nodo lab-small, poi tre nodi totali. Dopo il problema descritto sotto,
l'operatore umano ha scelto due nodi lab-medium per ripetere le prove.
Il nuovo cluster è stato creato da zero, senza volumi o workload preesistenti.

## Esercizio 1 — HPA

**Obiettivo:** dimostrare l'aumento automatico delle repliche sotto carico CPU
e la riduzione dopo la rimozione del carico, senza cambiare manualmente replicas.

Eseguiti: verifica delle metriche; applicazione di Deployment/Service e HPA;
attesa ScalingActive; lettura dello stato; generatore interattivo busybox:1.28
con il ciclo wget delle slide; osservazione HPA; Ctrl+C; attesa dello scale-down;
rimozione di HPA, Deployment e Service e verifica dei Pod eliminati.

| Osservazione | Esito |
| --- | --- |
| Baseline | Una replica, CPU 0%, target 50% |
| Crescita | 1 → 3 → 4 → 6; sei repliche disponibili |
| Carico | Risposte HTTP OK!; CPU fino al 187% della richiesta in una misura registrata |
| Stop | Ctrl+C circa 10:07:35; load-generator eliminato da --rm |
| Discesa | Sei → tre alle 10:12:38, richiesta di una alle 10:12:53 |
| Stato finale verificato | Una replica disponibile e CPU 0% alle 10:13:08 |
| Pulizia | Solo il Service Kubernetes rimane tra le risorse interrogate in default |

Il ritorno osservato a una replica ha richiesto circa 5 minuti e mezzo dopo
lo stop, coerente con la stabilizzazione predefinita. Non è stata abbreviata.
Le metriche inizialmente mancanti prima della prima raccolta sono transitorie;
dopo ScalingActive non sono ricomparsi i timeout dell'API del primo tentativo.

Evidenze: [setup](evidence/27-hpa-medium-setup.log),
[eventi HPA](evidence/30-hpa-medium-watch.jsonstream),
[crescita](evidence/32-hpa-medium-peak.log),
[discesa](evidence/33-hpa-medium-down.log),
[pulizia](evidence/35-hpa-cleanup-verified.log).
Il generatore è terminato con codice 130 dopo Ctrl+C, interruzione prevista.
L'osservatore è stato chiuso dopo il completamento della prova.

## Esercizio 3 — CRD e custom object

**Obiettivo:** estendere l'API con un tipo e distinguere un custom object
dall'automazione che richiederebbe un controller.

Eseguiti: apply della CRD, wait e describe con Established=True;
apply dell'istanza, get e describe di my-new-cron-object; ispezione di
Pod/Job/CronJob; cancellazione prima dell'oggetto e poi della CRD.

Esito: cronSpec e image conservati nell'oggetto; nessun Pod, Job o CronJob
creato. Al termine non rimangono né oggetti CronTab né la loro definizione.
Comandi conclusi con codice 0. La prova sul nuovo cluster è iniziata solo
dopo il completamento della pulizia HPA.

Evidenza: [36-crd-medium.log](evidence/36-crd-medium.log).
Anche la prima esecuzione aveva completato questa prova:
[07-crd.log](evidence/07-crd.log).

## Esercizio 2 — MySQL Operator e InnoDB Cluster

**Obiettivo:** osservare un operatore che traduce un custom object in risorse
applicative, attenderne la convergenza e collegarsi attraverso il Router.

Eseguiti: installazione delle quattro CRD upstream, attesa Established,
installazione dell'operatore e rollout; Secret didattico; apply di mycluster;
watch; attesa di tre membri ONLINE; Router disponibile; MySQL Shell
interattiva, connessione root@mycluster, query, disconnessione e Ctrl+D.

Esito alle 10:19:34, circa 4 minuti e 10 secondi dopo la creazione:

- InnoDBCluster ONLINE, tre membri su tre; tutti i Pod MySQL 2/2 e zero riavvii.
- Router 1/1; tre PVC Bound da 2 GiB su cloud-standard.
- Connessione tramite Service mycluster:3306 con SSL; server raggiunto mycluster-0,
  versione 9.1.0.
- Tre righe nella membership: mycluster-0 PRIMARY, mycluster-1 e mycluster-2
  SECONDARY, tutte ONLINE.
- Due istanze sul worker e una sul control plane; non è una distribuzione su
  tre nodi distinti e non dimostra tolleranza ai guasti di nodo.

Lo stato ONLINE è comparso anche con due membri: per questo la guida controlla
esplicitamente onlineInstances=3 e availableReplicas=1 del Router.
L'ultimo init container ha terminato regolarmente con codice 0; i passaggi
PENDING/INITIALIZING/ONLINE_PARTIAL erano parte della convergenza.

Pulizia eseguita con l'operatore ancora attivo: custom object eliminato,
attesa della scomparsa dei tre Pod, Secret mypwds eliminato.
I PVC sono rimasti dopo la cancellazione dell'InnoDBCluster: rimossi quindi
i soli tre PVC registrati e verificata la scomparsa dei corrispondenti PV
e volumi cloud. Infine rimossi operatore, namespace dedicato, RBAC e CRD.
MySQL Shell è uscita con codice 0 e il Pod myshell è stato rimosso da --rm.

Evidenze: [operatore](evidence/37-mysql-medium-operator.log),
[cluster pronto](evidence/38-mysql-medium-cluster.log),
[sessione SQL integrale](evidence/42-mysql-shell.typescript),
[tabelle SQL estratte](evidence/42-mysql-results.txt),
[pulizia oggetti](evidence/43-mysql-cleanup-objects.log),
[pulizia storage e operatore](evidence/44-mysql-cleanup-volumes-operator.log).

## Primo tentativo: problema, consultazione e rimedio

Il primo cluster aveva tre lab-small da 2 GiB. HPA aveva raggiunto cinque
repliche; durante la sua attesa di scale-down era stato avviato MySQL.
Questa sovrapposizione non è richiesta dalle slide ed è stata eliminata
dalla procedura definitiva.

Sono comparsi timeout dell'API, errori della Metrics API e metriche HPA
sconosciute. L'operatore ha prima autorizzato la sola osservazione.
Le interruzioni sono proseguite e lo scale-down non è stato verificato:
l'attesa è scaduta. MySQL era arrivato a due membri online; nessuna connessione
SQL era stata eseguita su quel cluster. Le letture diagnostiche con kubectl
host v1.37.2 appartengono a questo tentativo, non alla prova definitiva.

Diagnosi in sola lettura: memory.current 2.147.401.728 su memory.max
2.147.483.648 byte, contatore max 817148, oom_kill=0. mysqld usava
808.040 KiB RSS e il sidecar 138.640 KiB RSS. La pressione di memoria era osservata;
non è stata dimostrata come unica causa possibile dei timeout.

Dopo l'autorizzazione a separare gli esercizi, anche la cancellazione
HPA via API è fallita con TLS handshake timeout (uscita 1).
L'operatore ha quindi autorizzato la cancellazione del cluster di prova e
dei suoi tre volumi e la creazione di due nodi lab-medium.

Il provider si era fermato sull'arresto ordinato del control plane
(`docker exec ... sudo init 0`). Un `docker stop --timeout 10 lecture03`
mirato al nodo già destinato alla cancellazione ha consentito di completarla.
I tre volumi, diventati Available/Retain, sono stati rimossi per ID e il
budget è tornato a 20 GiB liberi prima della ricreazione.

Rimedio **provato con successo**: due lab-medium ed esperimenti sequenziali.
Poiché sono cambiate entrambe le condizioni, la prova non isola l'effetto
della sola taglia macchina da quello della mancata sovrapposizione.
La variante MySQL isolata su tre lab-small non è stata provata.

Evidenze: [timeout](evidence/19-diagnostics.log),
[memoria](evidence/21-node-pressure.log),
[cancellazione via API fallita](evidence/23-hpa-cleanup.log),
[rimozione cluster](evidence/24-delete-small-cluster.log),
[arresto mirato](evidence/25-stop-unresponsive-node.log),
[volumi e ricreazione](evidence/26-recreate-medium.log).

## Stato dopo HPA / CRD / MySQL, adattamenti e limiti

[Verifica finale](evidence/45-final-state.log): due nodi Ready; nessun Pod,
Deployment, StatefulSet, ReplicaSet, HPA, PVC o Secret degli esercizi in default;
nessuna CRD o PV; namespace mysql-operator assente; nessun volume cloud;
storage 0 allocato / 20 GiB disponibile. In default restano solo gli oggetti
di base interrogati: Service kubernetes, ConfigMap kube-root-ca.crt e
ServiceAccount default. I container temporanei dei comandi sono chiusi.
Cluster e servizi del provider restano accesi, pronti per altre prove.

Adattamenti rispetto alle slide: verifica del Metrics Server del provider
in luogo dell'addon minikube host; trattini watch corretti; file upstream
versionati conservati localmente; client MySQL fissato al tag dell'operatore;
attese esplicite, query di controllo e cleanup completo. I manifest originali
php-apache, hpa e mycluster sono rimasti identici byte per byte.

Perimetro iniziale, prima dell’aggiunta CA: tre esercizi visibili; slide HPA duplicate trattate come un esercizio.
PostgreSQL è nelle slide nascoste; VPA, KEDA e Cluster Autoscaler non hanno
esperimenti assegnati nel deck originale. Il nuovo incarico CA è documentato sotto.
In questa prima fase non erano stati modificati i PPTX o il codice del provider. Non sono stati
eseguiti failover, backup, aggiornamenti MySQL né prove su amd64.
Le modifiche preesistenti ai due file di lecture02 sono rimaste estranee al lavoro.

Verifica statica: sette manifest YAML, diciassette oggetti; selettori e target
HPA/Service/Deployment coerenti, API e kind CR/CRD coerenti, checksum registrati
in SHA256SUMS. La verifica statica integra le prove reali, non le sostituisce.


## Esercizio aggiunto — Cluster autoscaling

Su richiesta dell’operatore sono state inserite sei slide (9–14) dopo HPA,
con setup, installazione, scale-up, scale-down, cleanup e domande di verifica.
Riusati `lab install/uninstall autoscaler`, `lab load start/stop` e i manifest
esistenti in `lab/manifests`; nessuna modifica al codice del provider.
Le altre slide, incluse le due nascoste, mantengono contenuto e stile originali.

Il cluster precedente era vuoto e senza volumi. L’operatore ha autorizzato
la sua sostituzione e ha scelto di lasciare il cluster al minimo dopo la prova.
`DELETE_CLUSTER op-000048` completata senza interventi manuali; creato
`lecture03 --machine-type lab-small --min-nodes 1 --max-nodes 3`
con `CREATE_CLUSTER op-000049`. Cluster Autoscaler v1.35.0; server v1.35.6;
comandi eseguiti con i wrapper del progetto.

| Passaggio (UTC, 11/11/2026) | Evidenza osservata |
| --- | --- |
| 10:37:29 | Inizio creazione; un control plane, autoscaler disponibile prima del carico |
| 10:39:06 circa | Quattro Pod richiesti, tre Pending, eventi `Insufficient cpu` |
| 10:39:17 | `op-000050` e `op-000051`, ADD_MACHINE da cluster-autoscaler |
| 10:39:45 | Quattro repliche Running; worker m02 Ready, m03 ancora in assestamento |
| Prima dello stop | Tutti e tre i nodi Ready; entrambe le operazioni ADD_MACHINE DONE |
| 10:40:06 | `lab load stop` completato |
| 10:41:03 | DELETE_MACHINE m03, `op-000052`, DONE in circa 4 secondi |
| 10:42:04 | DELETE_MACHINE m02, `op-000053`, DONE in circa 4 secondi |
| 10:42:50 | Solo control plane Ready; nessuna operazione in corso; min size reached nei log |
| 10:44:32 | Autoscaler disinstallato; nessun lab-load, PV, PVC, CRD o volume cloud |

La traiettoria osservata è **1 → 3 → 1 nodi**. Il terzo nodo durante lo scale-up
è previsto come possibilità dal tutorial; le slide richiedono almeno due nodi
Ready, non esattamente due. Circa 40 secondi per rendere disponibili tutte le
repliche, circa due minuti fra stop del carico e completamento dell’ultima
rimozione cloud. I tempi sono osservazioni della singola prova, non garanzie.
Nessun timeout API, riavvio dei Pod di sistema o errore operativo osservato.

Stato lasciato: `lecture03` RUNNING, lab-small, MIN 1 / MAX 3, un solo nodo
Ready; provider, NFS, portal e dashboard attivi. Memoria del nodo circa
1019 MiB su 2 GiB nell’ultima misura. Autoscaler e carico rimossi.
Non è stato ripristinato il cluster lab-medium, come richiesto dall’operatore.
La variante opzionale del tutorial a dieci repliche non è stata eseguita.

Evidenze locali: [rimozione](evidence/51-ca-recreate.log),
[setup](evidence/52-ca-setup.log), [scale-up](evidence/53-ca-scale-up.log),
[log autoscaler e stop](evidence/55-ca-scale-down-start.log),
[scale-down](evidence/56-ca-scale-down-observe.log),
[cleanup finale](evidence/57-ca-cleanup.log).
La guida eseguibile è [CLUSTER_AUTOSCALING.md](CLUSTER_AUTOSCALING.md).


## Esercizio aggiunto — KEDA Cron scaler

Su richiesta dell’operatore è stato cercato un tutorial semplice in rete.
Scelta: [esempio ufficiale Cron scaler](https://keda.sh/docs/2.21/scalers/cron/),
seguendo [Setup Autoscaling](https://keda.sh/docs/2.21/setupscaler/) e adattando
il Deployment del [tutorial HTTP](https://keda.sh/docs/2.21/deploy/).
Il segnale è una finestra oraria; non servono broker, credenziali esterne o
Prometheus. La scelta dimostra ScaledObject, HPA e scale-to-zero, non un trigger
su coda né il consumo di eventi applicativi.

Importata la release ufficiale KEDA v2.21.0 (26 oggetti) e relativa licenza
Apache-2.0. SHA-256 del manifest verificato anche contro il digest della
release GitHub: `b43c89ffeef81722d7e2dd2c079d74789767a0f89cae1336cff784994814f6d7`.
La compatibilità ufficiale include Kubernetes 1.35; il manifest completo con
webhook è stato applicato senza modifiche con server-side apply.
Esempi adattati e fonti/licenze sono descritti in [keda/README.md](keda/README.md).

Cluster riutilizzato solo dopo cleanup CA: un lab-small Ready, nessun carico
precedente, KEDA e API external.metrics assenti prima dell’installazione.
Tre Deployment KEDA disponibili e API esterna Available alle 10:49:30 UTC.
Un riavvio iniziale dell’operatore è stato esaminato: il log dichiara l’uscita
volontaria dopo l’aggiornamento dei Secret dei certificati (`RestartOnSecretRefresh`).
Nessuna correzione o riavvio manuale necessario. Non è un OOM osservato.

Deployment `keda-http` con `hashicorp/http-echo:1.0.0`, disponibile per ARM64;
requests 25m/16Mi e limits 100m/64Mi, readiness probe HTTP. La replica iniziale
è stata attesa prima della finestra per separare il download dell’immagine
dallo scaling. ScaledObject con min 0 / max 2, polling 5s, cooldown 20s,
`Etc/UTC`, start 10:53 ed end 10:55.

| Osservazione UTC | Stato |
| --- | --- |
| 10:51:31 | ScaledObject Ready; KEDA crea l’HPA e porta il target iniziale da 1 a 0 |
| 10:51:47–10:52:51 | Zero repliche; Active=False |
| 10:53:01 (log operatore) | KEDA attiva il Deployment da 0 a 1 |
| 10:53:24 | Due repliche disponibili; Active=True; evento HPA SuccessfulRescale a 2 |
| 10:55:01 | Finestra chiusa; Active=False; due repliche ancora presenti |
| 10:55:16 (log operatore) | KEDA porta il Deployment da 2 a 0 |
| 10:55:17 e campioni successivi | Zero repliche confermate |

Il cooldown decorre dall’ultima rilevazione attiva, campionata ogni 5 secondi:
non promettere esattamente 20 secondi dopo il minuto end. L’HPA mostra minimo 1;
il controllo dello zero è di KEDA. `<unknown>` sulle metriche HPA a zero è
stato osservato insieme a ScaledObject Ready/Active=False e Deployment a zero,
non interpretato da solo come fallimento. Non sono stati modificati manualmente
replicas durante il ciclo. Il numero di nodi è rimasto uno.

Cinque slide KEDA inserite immediatamente dopo CA, ora 15–19. Deck finale:
33 slide totali, due ancora nascoste; contenuto delle 22 slide originali
preservato. Le undici slide nuove sono state aperte e controllate in PowerPoint.
La variante Redis/queue e l’esempio HTTP/Prometheus non sono stati eseguiti.

Evidenze: [preflight](evidence/58-keda-preflight.log),
[installazione](evidence/59-keda-install.log),
[riavvio certificati](evidence/60-keda-startup-diagnostics.log),
[workload](evidence/61-keda-workload.log),
[finestra](evidence/62-keda-window.txt),
[scaling](evidence/63-keda-scaling.log),
[dettagli ed eventi](evidence/64-keda-result-details.log).


### Stato finale dopo tutti gli esercizi

Cleanup KEDA completato alle **10:58:09 UTC**: prima ScaledObject e HPA,
poi Deployment didattico, infine i 26 oggetti dell’installazione KEDA.
La lettura di tutti gli oggetti del manifest con `--ignore-not-found`
non ha restituito risorse. Namespace keda, CRD KEDA e API external.metrics
assenti; nessun workload, HPA o PVC in default; nessun volume cloud.
Il nodo `lecture03` lab-small è Ready, i Pod di sistema hanno zero riavvii.
Il provider mantiene MIN 1 / MAX 3; Cluster Autoscaler è disinstallato.
Cluster, dashboard e servizi cloud restano attivi, come richiesto.
Evidenza: [cleanup KEDA e stato finale](evidence/65-keda-cleanup.log).

Verifica dei materiali finali: dieci YAML, quarantacinque oggetti; checksum
validi, manifest release identico al digest GitHub, riferimenti Markdown
locali risolti, target Deployment/ScaledObject coerenti. Deck finale verificato
in PowerPoint; font, layout e risorse delle slide originali preservati.


## Ripetizione KEDA — 11 novembre 2026, 12:26–12:32 UTC

Ripetuta l’installazione da zero usando i manifest consegnati e i comandi
della guida, sullo stesso nodo lab-small precedentemente pulito. Contesto
lecture03 e namespace default verificati prima della prova.
Finestra aggiornata in `keda/scaledobject.yaml`: **12:29–12:31 UTC**;
aggiornato il relativo SHA-256 in `SHA256SUMS`.

- Prima della finestra: zero repliche, Ready=True e Active=False.
- 12:29:10: una replica disponibile, Active=True.
- 12:29:22: due repliche disponibili; HPA posseduto dallo ScaledObject verificato.
- Risposta HTTP verificata tramite port-forward temporaneo su localhost:
  HTTP 200 e corpo `Hello, KEDA!`. Inoltro chiuso subito dopo la verifica.
- 12:31:08: Active=False, due repliche ancora presenti durante il cooldown.
- 12:31:19: zero repliche disponibili e richieste, ciclo completo verificato
  con assert automatici sui campioni JSON. Nessuno scaling manuale.
- 12:32:02: cleanup completo; oggetti del manifest upstream assenti, nessuna
  CRD, API external.metrics, namespace keda, Pod/HPA/PVC didattico o volume cloud.
  Rimane un nodo lecture03 lab-small Ready.

Questa installazione ha mostrato un riavvio iniziale per ciascuno dei tre
componenti KEDA: l’operatore è uscito volontariamente dopo l’aggiornamento dei
certificati; metrics-apiserver e admission webhook hanno inizialmente trovato
assenti rispettivamente `/certs/ca.crt` e `/certs/tls.crt`. Poi tutti i rollout e
l’API esterna sono diventati disponibili, con contatori stabili per il resto
della prova. La guida è stata corretta: verificare causa, convergenza e stabilità,
non assumere che si riavvii soltanto l’operatore. Aggiunto anche il controllo
esplicito di contesto e namespace. Nessuna modifica al manifest upstream o al
codice del provider è stata necessaria.

Le slide KEDA 15–19 sono state ricontrollate rispetto ai manifest: nomi,
versione, limite 0–2 e cooldown sono coerenti; nessuna modifica al deck richiesta
dai risultati della ripetizione. Cluster Autoscaler, CRD e MySQL non sono stati
rieseguiti in questa ripetizione.

Evidenze: [installazione](evidence/keda-rerun-20261111-1226/02-install.log),
[diagnosi iniziale](evidence/keda-rerun-20261111-1226/03-startup.log),
[ciclo con assert](evidence/keda-rerun-20261111-1226/04-cycle.log),
[HTTP](evidence/keda-rerun-20261111-1226/05-http.txt),
[cleanup](evidence/keda-rerun-20261111-1226/07-cleanup.log).
I campioni JSON e lo script di osservazione sono conservati nella stessa
cartella locale `evidence/`, esclusa da Git.


## Ripetizione Cluster Autoscaling con kubectl — 11 novembre 2026

Riviste le slide 9–14 e la guida per creare tutte le risorse Kubernetes con
`kubectl apply`, dalla cartella `kubernetes/lecture03`. I tre manifest in
`autoscaler/` sono copie identiche di `lab/manifests/autoscaler/`; `load.yaml`
differisce da `lab/manifests/load.yaml` soltanto per `replicas: 0`.
Il carico si controlla con `kubectl -n default scale deployment/lab-load`
a quattro e poi a zero repliche. Nessun HPA: lo studente sceglie le repliche,
il Deployment controller le mantiene e Cluster Autoscaler adegua i nodi.
Non riapplicare il manifest a zero durante la fase di carico.

| Osservazione UTC | Risultato |
| --- | --- |
| 12:38:17 | Preflight: un lab-small Ready, intervallo MIN 1 / MAX 3, nessun carico precedente |
| 12:39:05 | Applicati i sette oggetti autoscaler locali e il Deployment iniziale a zero |
| 12:39–12:40 | Scale a quattro: tre Pod Pending per CPU insufficiente, poi quattro disponibili e tre nodi Ready |
| 12:39:34 | ADD_MACHINE op-000054 e op-000055, requester cluster-autoscaler, entrambi DONE |
| 12:40:46 | Scale a zero completato; Deployment conservato, nessun Pod di carico |
| 12:41:21 | DELETE_MACHINE op-000056 per m03, DONE in 4 secondi |
| 12:42:21 | DELETE_MACHINE op-000057 per m02, DONE in 4 secondi; ritorno a un nodo |
| 12:46:30 | Cleanup completo: workload e autoscaler assenti, nessun volume cloud, un lab-small Ready |

Il preflight aveva rilevato due oggetti generati dal controller nella prima
prova: ConfigMap `cluster-autoscaler-status` e Lease `cluster-autoscaler`.
Non sono inclusi nei manifest upstream e il solo `delete -f autoscaler/`
non li rimuove. La procedura ora cancella il controller con
`--cascade=foreground` e poi elimina esplicitamente questi due oggetti.
La sequenza è stata eseguita e l’assenza di entrambi verificata.
Nessun errore operativo o timeout API osservato in questa ripetizione.
Il provider resta attivo con MIN 1 / MAX 3 e un solo nodo; non sono stati
rieseguiti KEDA, HPA, CRD o MySQL durante questa revisione.

Le sei slide modificate sono state controllate visivamente in PowerPoint.
Il deck conserva 33 slide e tutti i contenuti delle altre slide; aggiornate
anche le note delle sei slide. Le quattro copie locali dei manifest sono
incluse in `SHA256SUMS`.

Evidenze locali (cartella esclusa da Git):
[preflight](evidence/ca-kubectl-20261111/01-preflight.log),
[creazione e scale-up](evidence/ca-kubectl-20261111/02-scale-up.log),
[scale-down](evidence/ca-kubectl-20261111/03-scale-down.log),
[convergenza](evidence/ca-kubectl-20261111/04-convergence.log),
[cleanup e verifica DONE finale](evidence/ca-kubectl-20261111/05-cleanup.log).
