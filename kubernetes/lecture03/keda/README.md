# KEDA — servizio attivo in una finestra temporale

Esercizio semplice per osservare **0 → 2 → 0 repliche** mantenendo un solo nodo.
Il segnale è l'apertura/chiusura di una finestra oraria: non servono broker,
account esterni o un generatore di carico. KEDA registra il tipo `ScaledObject`,
riconcilia la configurazione e crea un HPA. KEDA gestisce l'attivazione da zero
e il ritorno a zero; l'HPA gestisce lo scaling mentre il workload è attivo.

## Fonti e file importati

- Tutorial ufficiale [Setup Autoscaling with KEDA](https://keda.sh/docs/2.21/setupscaler/).
- Esempio ufficiale [Cron scaler: fixed number of replicas](https://keda.sh/docs/2.21/scalers/cron/#example-fixed-number-of-replicas-scale-up-to-10-replicas-from-6am-to-8pm-and-scale-down-to-0-replicas-otherwise).
- Deployment adattato dal [servizio HTTP del tutorial](https://keda.sh/docs/2.21/deploy/#step-1-deploy-a-sample-application).
- [Manifest di release v2.21.0](https://github.com/kedacore/keda/releases/download/v2.21.0/keda-2.21.0.yaml), importato integralmente e senza modifiche in `upstream/`, con licenza Apache-2.0 in `upstream/LICENSE`.
- La documentazione KEDA è CC-BY-4.0, © KEDA Authors: i due manifest didattici sono adattamenti con attribuzione, non copie identiche del tutorial.

Adattamenti: nome `keda-http`; immagine HTTP echo fissata a `1.0.0` e verificata
per ARM64; requests/limits e readiness probe; nessun Service necessario per
osservare le repliche; trigger Cron al posto dell'esempio HTTP/Prometheus;
UTC, due repliche massime, polling 5 s e cooldown 20 s per una dimostrazione breve.
Il tutorial HTTP completo richiederebbe anche Prometheus e metriche applicative.

KEDA 2.21 è testata upstream con Kubernetes 1.34–1.36
([compatibilità](https://keda.sh/docs/2.21/operate/cluster/#kubernetes-compatibility));
il laboratorio usa Kubernetes 1.35.6. Le immagini della release sono fissate.

## 1. Preparare il cluster e installare KEDA

Prima completare lo scale-down del Cluster Autoscaler, disinstallarlo e rimuovere
il carico. Riutilizzare il nodo lab-small pulito. Dalla radice del progetto:

```sh
source scripts/configure.sh
kubectl config current-context
kubectl config view --minify -o 'jsonpath={..namespace}'
kubectl get nodes
kubectl get deployment,pods,hpa,pvc -n default
kubectl get ns keda --ignore-not-found
kubectl get crd
kubectl get apiservice v1beta1.external.metrics.k8s.io --ignore-not-found
cd kubernetes/lecture03/keda
```

Controllare che il contesto sia `lecture03` (o il cluster dedicato scelto) e
che il namespace corrente sia `default`; un valore vuoto indica `default`.
I manifest dell’applicazione indicano quel namespace, e i comandi di
osservazione qui sotto lo assumono. Se è diverso, usare `-n default` nei comandi
dell’applicazione.

Se KEDA o un altro adapter per external.metrics esistono già, fermarsi e consultare
l'operatore: questa procedura installa e poi rimuove una propria istanza dedicata.
Il Metrics Server di Kubernetes (`metrics.k8s.io`) resta al suo posto.

```sh
kubectl apply --server-side -f upstream/keda-2.21.0.yaml
kubectl -n keda rollout status deployment/keda-operator --timeout=5m
kubectl -n keda rollout status deployment/keda-metrics-apiserver --timeout=5m
kubectl -n keda rollout status deployment/keda-admission --timeout=5m
kubectl wait --for=condition=Available apiservice/v1beta1.external.metrics.k8s.io --timeout=120s
```

Checkpoint: tre Deployment disponibili e API esterna Available. Durante la
prima generazione dei certificati, l’operatore può uscire volontariamente dopo
l’aggiornamento dei Secret. Anche webhook e metrics-apiserver possono riavviarsi
se partono prima che `/certs/tls.crt` e `/certs/ca.crt` siano disponibili: è stato
osservato nella ripetizione della prova. Non assumere che qualunque riavvio sia
innocuo; controllarne la causa:

```sh
# Usare --previous solo per i componenti che risultano riavviati:
kubectl -n keda logs deployment/keda-operator --previous
kubectl -n keda logs deployment/keda-metrics-apiserver --previous
kubectl -n keda logs deployment/keda-admission --previous
kubectl -n keda get pods
```

Procedere solo dopo i rollout e l’API Available, con contatori dei riavvii
stabili. Se i componenti non convergono o gli errori persistono, conservare i log
e consultare l’operatore, senza disabilitare webhook o controlli TLS.

## 2. Avviare l'applicazione e scegliere gli orari

```sh
kubectl apply -f deployment.yaml
kubectl rollout status deployment/keda-http --timeout=3m
date -u '+%H:%M UTC'
```

Attendere prima il download dell'immagine. **Ad ogni esecuzione**, modificare
in `scaledobject.yaml` i due orari: `start` circa due minuti nel futuro e `end`
circa due minuti dopo `start`, lasciando `timezone: Etc/UTC`.
I campi cron sono `minuto ora giorno-del-mese mese giorno-della-settimana`.
Per esempio, se ora sono le 10:50 UTC:

```yaml
start: "52 10 * * *"
end: "54 10 * * *"
```

La finestra si ripete ogni giorno: il manifest non è un timer relativo.
Non riutilizzare gli orari della prova registrata senza aggiornarli.
Start ed end devono essere diversi. Il Cron scaler mantiene un Deployment
attivo durante la finestra; non avvia Job periodici come un CronJob.

## 3. Osservare zero, attivazione e ritorno a zero

```sh
kubectl apply -f scaledobject.yaml
kubectl wait --for=condition=Ready scaledobject/keda-http-window --timeout=60s
kubectl get deployment keda-http -w
```

In un altro terminale, dopo `source scripts/configure.sh` dalla radice:

```sh
kubectl get scaledobjects,hpa
kubectl get pods -l app=keda-http
kubectl describe scaledobject keda-http-window
```

Checkpoint da registrare, senza modificare manualmente `replicas`:

1. Prima di start: `Ready=True`, `Active=False`, Deployment a zero.
2. Durante la finestra: `Active=True`, Deployment con due repliche disponibili;
   HPA `keda-hpa-keda-http-window` creato da KEDA.
3. Dopo la chiusura e il cooldown: `Active=False`, Deployment di nuovo a zero e nessun
   Pod keda-http. Polling e riconciliazione possono aggiungere alcuni secondi.

Il cooldown di 20 s decorre dall’ultima rilevazione attiva (polling ogni 5 s)
e riguarda il ritorno a zero, non sostituisce la finestra
HPA di scale-down tra repliche positive. L'HPA può mostrare minimo 1 o
`ScalingDisabled` quando KEDA porta il Deployment a zero: osservare il
Deployment e lo ScaledObject insieme. Non creare un secondo HPA sullo stesso
Deployment. Non riapplicare `deployment.yaml` durante la prova: contiene
`replicas: 1` per la verifica iniziale.

Diagnosi in caso di mancata convergenza:

```sh
kubectl describe scaledobject keda-http-window
kubectl describe hpa keda-hpa-keda-http-window
kubectl -n keda logs deployment/keda-operator --since=10m
kubectl get events --sort-by=.lastTimestamp
kubectl top nodes
```

Consultare l'operatore se la prova fallisce; conservare orari e log.

## 4. Cleanup prima dell'esercizio successivo

Dopo aver osservato il ritorno a zero, terminare i watch con Ctrl+C.
Eliminare lo ScaledObject mentre KEDA è ancora attivo:

```sh
kubectl delete -f scaledobject.yaml
kubectl wait --for=delete hpa/keda-hpa-keda-http-window --timeout=60s
kubectl delete -f deployment.yaml
kubectl get pods,hpa -n default
# Solo l'installazione KEDA creata per questa prova:
kubectl delete -f upstream/keda-2.21.0.yaml --timeout=180s
kubectl get crd
kubectl get apiservice v1beta1.external.metrics.k8s.io --ignore-not-found
kubectl get ns keda --ignore-not-found
kubectl get nodes
```

Checkpoint: ScaledObject, HPA, Deployment e Pod didattici assenti; namespace,
CRD, webhook e API esterna KEDA rimossi; il nodo rimane Ready. Il manifest
upstream include oggetti di cluster: non usarne la cancellazione su KEDA condivisa.

Prima di MySQL utilizzare il setup a due lab-medium di [../README.md](../README.md),
dopo il cleanup. Risultati della prova in [../SESSION.md](../SESSION.md).


## Prova registrata — 11 novembre 2026

Finestra usata: 10:53–10:55 UTC. Deployment a zero prima dell’apertura,
una replica alle 10:53:07, due disponibili alle 10:53:24; `Active=False`
alle 10:55:01 e ritorno a zero osservato alle 10:55:17.
Il polling campiona il comportamento: gli orari sono osservazioni, non
la misura esatta dell’istante di transizione. Il singolo nodo è rimasto Ready.
La partenza da una replica è servita solo a verificare l’immagine; durante
il ciclo KEDA non è stato eseguito alcuno scaling manuale.

Log: [finestra e manifest](../evidence/62-keda-window.txt),
[sequenza di scaling](../evidence/63-keda-scaling.log),
[condizioni ed eventi](../evidence/64-keda-result-details.log).

Cleanup completo verificato alle 10:58:09 UTC: [log finale](../evidence/65-keda-cleanup.log).


### Seconda prova — 11 novembre 2026

Ripetuto il ciclo con finestra **12:29–12:31 UTC**: due repliche disponibili
alle 12:29:22, ritorno a zero alle 12:31:19. Verificata anche la risposta HTTP
`Hello, KEDA!`. Cleanup concluso alle 12:32:02; rimane il nodo lab-small Ready.
I manifest conservano gli orari di questa seconda prova: aggiornarli prima
della prossima esecuzione.

Evidenze: [ciclo](../evidence/keda-rerun-20261111-1226/04-cycle.log),
[HTTP](../evidence/keda-rerun-20261111-1226/05-http.txt),
[cleanup](../evidence/keda-rerun-20261111-1226/07-cleanup.log).
