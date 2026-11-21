# MySQL Operator upstream manifests

These manifests come from tag `26.10.0-2.3.1`, updated at the lab operator's request.

- https://raw.githubusercontent.com/mysql/mysql-operator/26.10.0-2.3.1/deploy/deploy-crds.yaml
- https://raw.githubusercontent.com/mysql/mysql-operator/26.10.0-2.3.1/deploy/deploy-operator.yaml

The files are unmodified. Install the CRDs, wait for the Established condition and then apply the operator. See `../SHA256SUMS` for checksums and the upstream repository for the license and source.
https://github.com/mysql/mysql-operator/tree/26.10.0-2.3.1

These files create cluster-scoped objects and the mysql-operator namespace. Install or remove them only when no other exercise or application owns them. Apply the lecture03 manifests one exercise at a time.

Release notes: https://dev.mysql.com/doc/relnotes/mysql-operator/en/news-26-10-0-2.3.1.html

This release adds the `mysqlclustersetfailovers.mysql.oracle.com` CRD to the earlier course materials. The script waits for it to become Established. Server, Router and MySQL Shell default to 26.10.0.
