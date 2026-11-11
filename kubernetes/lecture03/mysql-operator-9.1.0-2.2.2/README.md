# MySQL Operator — manifest upstream

Copie dei manifest del tag `9.1.0-2.2.2`, lo stesso delle slide Kubernetes_5:

- https://raw.githubusercontent.com/mysql/mysql-operator/9.1.0-2.2.2/deploy/deploy-crds.yaml
- https://raw.githubusercontent.com/mysql/mysql-operator/9.1.0-2.2.2/deploy/deploy-operator.yaml

Non sono state applicate patch. Installare prima le CRD e attenderne la
condizione Established, poi applicare l'operatore. I checksum sono in
`../SHA256SUMS`. Consultare il repository upstream per licenza e sorgenti:
https://github.com/mysql/mysql-operator/tree/9.1.0-2.2.2

Questi file contengono oggetti cluster-scoped e il namespace mysql-operator.
Installazione e rimozione sono previste solo se tali risorse non appartengono
ad altre prove o applicazioni. Non includerli in un apply indiscriminato
dell'intera lecture03.
