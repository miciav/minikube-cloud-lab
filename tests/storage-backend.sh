#!/usr/bin/env bash
set -euo pipefail
storage_test_dir=$(mktemp -d)
trap 'rm -rf "$storage_test_dir"' EXIT
storage_test_arch=$(docker version --format '{{.Server.Arch}}')
GOOS=linux GOARCH="$storage_test_arch" go test -c ./internal/infra/nfs -o "$storage_test_dir/backend.test"
docker run --rm --network none \
  --cap-add SYS_ADMIN \
  --device-cgroup-rule 'b 7:* rwm' \
  --device-cgroup-rule 'c 10:237 rwm' \
  -e CLOUD_STORAGE_MOUNT_TEST=1 \
  --mount "type=bind,src=$storage_test_dir/backend.test,dst=/backend.test,readonly" \
  --entrypoint /backend.test "${NFS_IMAGE:-minikube-cloud-lab/nfs:dev}" -test.v
