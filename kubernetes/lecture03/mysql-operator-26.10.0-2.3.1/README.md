# MySQL Operator — manifest upstream

Copie dei manifest del tag `26.10.0-2.3.1`, aggiornato su richiesta dell’operatore:

- https://raw.githubusercontent.com/mysql/mysql-operator/26.10.0-2.3.1/deploy/deploy-crds.yaml
- https://raw.githubusercontent.com/mysql/mysql-operator/26.10.0-2.3.1/deploy/deploy-operator.yaml

Non sono state applicate patch. Installare prima le CRD e attenderne la
condizione Established, poi applicare l'operatore. I checksum sono in
`../SHA256SUMS`. Consultare il repository upstream per licenza e sorgenti:
https://github.com/mysql/mysql-operator/tree/26.10.0-2.3.1

Questi file contengono oggetti cluster-scoped e il namespace mysql-operator.
Installazione e rimozione sono previste solo se tali risorse non appartengono
ad altre prove o applicazioni. Non includerli in un apply indiscriminato
dell'intera lecture03.

Release notes: https://dev.mysql.com/doc/relnotes/mysql-operator/en/news-26-10-0-2.3.1.html

Questa release aggiunge rispetto ai materiali precedenti anche la CRD
`mysqlclustersetfailovers.mysql.oracle.com`; lo script ne attende Established.
Server, Router e MySQL Shell predefiniti: 26.10.0.
