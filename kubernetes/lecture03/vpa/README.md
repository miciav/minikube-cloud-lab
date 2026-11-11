# Vertical Pod Autoscaler: prima osservare, poi applicare

Obiettivo: aumentare la **richiesta CPU di ciascun Pod**, mantenendo due repliche.
Il carico è un ciclo CPU in BusyBox, senza servizi esterni o traffico da generare.
Si osservano prima le raccomandazioni (`Off`), poi la sostituzione dei Pod
(`Recreate`). Durata indicativa: 10–15 minuti, esclusa la creazione del cluster.

## 1. Preparazione

Usare il cluster dedicato `lecture03` con **due nodi lab-medium**, come nel
[setup della lezione](../README.md#setup-hpa--vpa--crd--mysql).
Rimuovere HPA, php-apache e load-generator dell'esercizio precedente.
Non installare un secondo VPA su un cluster che ne ha già uno.
Python 3 e OpenSSL devono essere disponibili sull'host.

Dalla radice del progetto, in ogni terminale:

```sh
source scripts/configure.sh
cloud clusters get-credentials lecture03
kubectl get nodes
kubectl top nodes
kubectl -n default get pods,deployments,hpa
kubectl get crd
cd kubernetes/lecture03/vpa
```

Checkpoint: due nodi Ready, metriche disponibili, nessun workload precedente,
nessuna CRD `verticalpodautoscalers.autoscaling.k8s.io` già installata.
Questo esercizio usa VPA 1.7.1; la prova è stata eseguita con Kubernetes 1.35.6.

## 2. Installare VPA

I sei manifest ufficiali sono inclusi in `upstream/`. Installare prima CRD e RBAC,
poi generare il Secret TLS del webhook, infine applicare l'intera cartella:

```sh
kubectl apply -f upstream/vpa-v1-crd-gen.yaml -f upstream/vpa-rbac.yaml
kubectl wait --for=condition=Established crd/verticalpodautoscalers.autoscaling.k8s.io crd/verticalpodautoscalercheckpoints.autoscaling.k8s.io --timeout=60s
python3 generate-tls.py | kubectl apply -f -
kubectl apply -f upstream/
kubectl -n kube-system rollout status deployment/vpa-recommender --timeout=5m
kubectl -n kube-system rollout status deployment/vpa-updater --timeout=5m
kubectl -n kube-system rollout status deployment/vpa-admission-controller --timeout=5m
kubectl get mutatingwebhookconfiguration vpa-webhook-config
```

Attendere che anche il webhook sia presente prima di continuare. Il piccolo helper
usa OpenSSL e una directory temporanea privata; emette solo il manifest JSON del
Secret da passare a `kubectl apply`. I certificati durano sette giorni e vanno
rigenerati a ogni installazione. Non salvare il suo output in Git o nei log.
L'admission controller registra il webhook con la CA corrispondente.

## 3. Osservare senza modificare i Pod

Leggere `deployment.yaml`: due repliche, ciascuna con request **50m CPU / 16Mi**
e limit **500m CPU / 64Mi**. 50m equivale a 0,05 core; il limite CPU contiene
il consumo del ciclo, mentre la request è usata per lo scheduling.

```sh
kubectl apply -f deployment.yaml
kubectl -n default rollout status deployment/vpa-demo --timeout=5m
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
kubectl apply -f vpa.yaml
kubectl -n default top pods -l app=vpa-demo
kubectl -n default describe vpa vpa-demo
```

Annotare nomi e UID iniziali. Ripetere `describe` finché appare una raccomandazione
CPU superiore a 100m; può richiedere alcuni minuti. La policy controlla soltanto
la request CPU (`RequestsOnly`), con minimo 100m e massimo 500m. La request iniziale
50m è volutamente sotto il minimo per rendere evidente l'aggiornamento successivo.

```sh
kubectl -n default get vpa vpa-demo -o 'jsonpath={.status.recommendation.containerRecommendations}'
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
```

**Checkpoint Off:** raccomandazione presente, stessi UID, request ancora 50m.
Nella prova il target era 500m, limitato dalla policy; il valore esatto dipende
dalle metriche raccolte. Una raccomandazione dopo pochi minuti serve alla demo,
non è una misura sufficiente per dimensionare un'applicazione in produzione.

## 4. Applicare con Recreate

```sh
kubectl -n default patch vpa vpa-demo --type=merge -p '{"spec":{"updatePolicy":{"updateMode":"Recreate"}}}'
kubectl -n default get pods -l app=vpa-demo -w
```

Attendere la sostituzione di entrambi i Pod e due nuovi Pod Ready, poi Ctrl+C.
Non cancellare o scalare manualmente i Pod: l'updater li espelle, il Deployment
li ricrea e l'admission controller modifica le richieste alla creazione.

```sh
kubectl -n default get events --field-selector reason=EvictedByVPA
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,UID:.metadata.uid,CPU:.spec.containers[0].resources.requests.cpu'
kubectl -n default get pods -l app=vpa-demo -o 'custom-columns=NAME:.metadata.name,MEM:.spec.containers[0].resources.requests.memory,CPU_LIMIT:.spec.containers[0].resources.limits.cpu,MEM_LIMIT:.spec.containers[0].resources.limits.memory'
kubectl -n default get deployment vpa-demo -o 'jsonpath={.spec.replicas}{" replicas; template CPU: "}{.spec.template.spec.containers[0].resources.requests.cpu}{"\n"}'
kubectl get nodes
```

**Checkpoint Recreate:** due nuovi UID, request CPU maggiore di 50m e non oltre
500m; memory request 16Mi e limiti 500m/64Mi invariati. Il Deployment continua
a dichiarare **due repliche e 50m nel template**; le nuove request sono sui Pod.
Il numero di nodi non cambia. Nella prova entrambe le request sono passate a 500m.

Domande: perché cambia l'UID? Quale componente decide la raccomandazione e quale
la applica? In cosa differiscono HPA (repliche), VPA (risorse per Pod) e Cluster
Autoscaler (nodi)? In questa prova non usare un HPA CPU sullo stesso workload.

## 5. Cleanup

Rimuovere prima il VPA e il workload, poi l'installazione e gli oggetti generati:

```sh
kubectl delete -f vpa.yaml -f deployment.yaml --cascade=foreground --timeout=180s
kubectl delete -f upstream/ --cascade=foreground --timeout=180s
kubectl delete mutatingwebhookconfiguration/vpa-webhook-config --ignore-not-found
kubectl -n kube-system delete secret/vpa-tls-certs lease/vpa-updater lease/vpa-recommender lease/vpa-recommender-lease --ignore-not-found
kubectl -n default get pods,deployments,hpa
kubectl get crd
kubectl -n kube-system get deployment vpa-recommender vpa-updater vpa-admission-controller --ignore-not-found
```

Checkpoint: nessun workload della prova, controller VPA o CRD VPA residuo.
Non applicare questa disinstallazione a un VPA condiviso con altri esercizi.
Se si prosegue con Cluster Autoscaler, seguire la sua guida per ricreare il
cluster vuoto con il dimensionamento previsto. **Alla fine del laboratorio**:

```sh
cloud clusters delete lecture03
cloud clusters list
cloud volumes list
```

Il test automatico del deck include questi checkpoint e cancella il cluster
alla fine: `python3 scripts/verify-lecture03.py --recreate-empty-cluster`
dalla radice del repository. In caso di errore conserva diagnostica e si ferma;
consultare l'operatore prima di riprovare o modificare il cluster.

## Fonti e provenienza

- [Quick start ufficiale VPA 1.7.1](https://github.com/kubernetes/autoscaler/blob/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/docs/quickstart.md): esempio hamster e osservazione delle request.
- [Installazione ufficiale](https://github.com/kubernetes/autoscaler/blob/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/docs/installation.md).
- `upstream/*.yaml`: copie senza modifiche dei sei file omonimi in [`vertical-pod-autoscaler/deploy/`](https://github.com/kubernetes/autoscaler/tree/vertical-pod-autoscaler-1.7.1/vertical-pod-autoscaler/deploy), tag `vertical-pod-autoscaler-1.7.1`; licenza Apache 2.0 inclusa. SHA256 in `../SHA256SUMS`.
- Adattamenti didattici: BusyBox multiarch con consumo limitato, CPU soltanto,
  modalità Off → Recreate, due repliche e TLS locale temporaneo. Nessun Helm,
  cert-manager, servizio esterno o aggiornamento in-place richiesto.
