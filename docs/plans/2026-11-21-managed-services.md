# Managed course services implementation plan

**Goal.** Students can use S3 object storage, an SQS-compatible queue and a relational database independently on the course VM. The annual acceptance command must demonstrate that uninstalling one service preserves its data and leaves the other services usable.

**Architecture.** Add three optional Compose profiles with the public names `object-storage`, `queue` and `database`. Use the existing sourced `lab` command for lifecycle management and standard clients for data operations. Each module works on rootless Podman and rootful Docker with zero Kubernetes nodes. Use Garage in single-node mode, ElasticMQ with its embedded persistent store and PostgreSQL. PostgreSQL provides the application experience of connecting to a managed SQL database. RDS administration APIs, DynamoDB, IAM and high availability are outside the first release.

**Execution.** Work natively from main at or after `26c8fb1`. Deliver the phases below in sequence. Each service gets its own independently testable change. Keep student-facing documentation in English.

## Student contract

| Operation | Observable result |
| --- | --- |
| `lab install NAME` | Starts only the requested module, initializes it on first use, waits for a bounded application-level readiness check and prints connection instructions. Repeating the command preserves endpoints, credentials and data. |
| `lab status NAME` | Reports disabled, starting, ready or degraded with a short reason. Distinguishes an unavailable runtime from a disabled module. Reports retained state separately from readiness. Never prints secrets. |
| `lab uninstall NAME` | Removes the selected runtime containers and releases their ports. Preserves named data volumes, access keys and connection settings. A second uninstall succeeds. |
| `lab purge NAME --yes` (proposed) | Explicitly erases that disabled module's owned data and credentials. Refuses while installed, without confirmation, or if ownership is ambiguous. A subsequent install starts fresh. |
| Client access | Install output gives the VM loopback endpoint and the path to a private client configuration. Examples use an explicit endpoint and lab-only credentials. A user can keep their existing AWS profile and database settings. |

Assume one trusted student or team per lab environment. A loopback port is reachable by other accounts on the same host. Verify this deployment assumption with IT before rollout, especially for ElasticMQ, whose SQS interface is not an IAM tenant boundary. If mutually untrusted students share the same VM, resolve isolation before assigning the queue exercise.

## 1. Prove storage, networking and client access on both runtimes

- Inspect `compose.yaml`, `compose.rootless.yaml`, `scripts/configure.sh`, `configure_test.go`, `compose_test.go` and the address reservations in `docs/operations.md`. The /23 serves minikube nodes, standalone instances, relays and instance autoscaling. Put the three services on a separate project-scoped bridge with a non-overlapping subnet; do not allocate more dynamic addresses on the existing lab bridge. Verify coexistence with a non-default lab subnet.
- Publish only the three application ports on VM loopback. Keep Garage administration/RPC and the queue administration UI private. Select distinct unprivileged defaults (candidates 3900, 9324 and 15432), allow overrides and fail clearly on collision. Decide the two client contexts up front: the VM shell uses localhost, and a course application on the services bridge uses service DNS. Use the public name, port and address meaningful to each context, including the QueueUrl ElasticMQ returns. Test that URL directly with the SDK from each context. Direct access from arbitrary Kubernetes Pods or standalone VMs is a separate routing exercise.
- Select release tags and immutable multi-architecture digests after checking supported images on linux/amd64 and linux/arm64. Verify fresh pulls, writable named volumes after UID mapping, available in-image probes and restart behavior with the actual `podman-compose` provider. Provide a pinned containerized client path for AWS CLI and psql so students can run the lessons without sudo or host client installations. These clients should be short-lived.
- Run a standalone persistence probe per engine with a fresh, owned volume. Write a value, remove the container, create a replacement with the same volume and read it back. Record the versions, architecture and result. In ElasticMQ explicitly enable `messages-storage.enabled = true` with H2 on the named volume. The queue-definition file alone only restores metadata. Test pending messages inside the configured retention period. Select a distribution that passes this probe before wiring in the queue. [ElasticMQ persistence](https://github.com/softwaremill/elasticmq#persisting-queues-and-messages-to-sql-database)
- Deliverable: verified version pins, storage paths, port mapping and a short repeatable smoke test. An untested architecture stays marked unverified.

## 2. Ship the lifecycle through one working module: Garage

1. Extend `configure_test.go` with a failing Bash/zsh wrapper test for `lab install object-storage`, status and uninstall. Include the Docker and Podman backends, an unknown name and a call to an existing module. Add a profile-isolation assertion in `compose_test.go`. Run `go test . -run 'Module|Wrapper'` and the tagged Compose tests. Confirm the new cases exercise the failing path.
2. Route a fixed allowlist through `scripts/configure.sh`. Put host-side operations in a small helper under `scripts/`; keep sourcing the shell free of runtime side effects. Update discovery and help in `internal/labcmd/root.go` and `internal/labcmd/module_status.go`. The direct binary can direct users to the configured shell, as it already does for instance autoscaling. Keep the existing Kubernetes and instance-group lifecycles intact. Avoid a plugin loader, a new daemon and a second cloud-state database.
3. Define a distinct profile and owned named volumes in both Compose files. Scope stop/removal to project and service; use the existing Podman label-selection pattern where Compose lacks `rm`. Never run project-wide teardown for a module. Serialize mutations on the same module, use atomic private secret-file writes, and obtain state from owned containers, volumes and real probes. Running install twice or retrying after an interrupted bootstrap must converge without replacing keys or deleting data.
4. Add Garage configuration and bootstrap under `lab/services/object-storage/`. Persist metadata, object data and the secrets needed to reopen the installation. Initialize the single-node layout once with replication factor one. Provision an exercise bucket, a read/write student key and a separate read-only key. Students receive S3 access; the bootstrap retains administrative capability. Use path-style addressing with an explicit region. [Garage quick start](https://garagehq.deuxfleurs.fr/documentation/quick-start/) · [S3 compatibility](https://garagehq.deuxfleurs.fr/documentation/reference-manual/s3-compatibility/)
5. Add `tests/managed-services.sh object-storage`. Use the student client to upload, list and download a known file; compare its checksum. Verify the read-only key can read and its write is denied. Uninstall and confirm the endpoint closes. Reinstall and repeat the read with the original credentials. Verify a new default lab starts no Garage container. Deliver this slice before adding the queue.

## 3. Add the independent SQS-compatible queue

- Add `lab/services/queue/`, an independent Compose profile, its own H2 volume and the same lifecycle verbs. Install it on an empty lab with Garage and PostgreSQL disabled. Use a lab-only AWS profile for signing with explicit endpoint selection. The queue may use dummy client credentials. Describe their actual security semantics in the lesson.
- Let students create a standard queue using `aws sqs create-queue`, send a message, receive it and delete it using its receipt handle. Record the QueueUrl returned by the service. Verify redelivery after a short visibility timeout using bounded polling. Use the stable application job ID to compare deliveries, since receipt handles change.
- Add `tests/managed-services.sh queue`. Send a second pending message, uninstall/reinstall, fetch and acknowledge it, and verify it is no longer delivered after the visibility window. Run the probe on both runtimes. Add failure tests for an unwritable store and a busy port; each should produce a non-ready status and preserve previous data.

## 4. Add the independent PostgreSQL service

- Add `lab/services/database/`, a Compose profile and a named volume mounted at the persistent path of the pinned PostgreSQL major version. On first install provision one exercise database and a non-superuser application role. Generate private credentials per installation. Keep bootstrap credentials separate from student configuration. Document how the VM user runs psql and obtains a connection string for an application without printing its password into normal logs. [PostgreSQL roles](https://www.postgresql.org/docs/current/database-roles.html)
- Use an authenticated `SELECT 1` for readiness. Add `tests/managed-services.sh database`. As the student, create a table, insert and commit a row, reconnect and read it. Verify an unprivileged role cannot create a superuser. Remove/reinstall the module and read the same row with the previous credentials.
- Interrupt first initialization and retry. Verify that a nonempty volume with missing or mismatched credentials is reported as degraded with a recovery instruction. The installer must never silently reset it. Pin the database major version and leave major upgrades as an explicit instructor operation.

## 5. Prove isolation, persistence and cleanup

Extend the same test entry point with `all`. Exercise each module alone from a clean install, then install all three. Run one case per purge target. At the start of each case, seed all three services with a case-specific object key, queue and message, and row ID. Use those fixtures throughout that case.

1. Uninstall it. Check its container is absent and port released. Perform a real application read/write against both surviving services.
2. Reinstall it. Verify the original payload and credentials, using a zero visibility timeout for queue reads and leaving the fixture message unacknowledged so later checks can read it again. Restart the service process/container and verify readiness recovers without enabling a disabled sibling.
3. Attempt purge while installed and expect rejection. Uninstall, purge the target and reinstall. Verify that the target’s fixture is absent after reinstall and read back both siblings’ fixtures from this case before starting the next one.

Test repeated install/uninstall, interrupted install and concurrent same-module commands. Simulate a runtime inventory error; it must fail the test rather than count as an empty installation. Match ownership by project and service, record only resources created by the current run for cleanup, and refuse ambiguous existing data. On failure retain the failed step and diagnostic paths. Keep credentials, tokens, passwords and object contents out of reports. Review `compose down -v` and reset documentation to reflect their destructive scope.

## 6. Deliver the lessons and the combined exercise

- Create `docs/tutorials/storage/object-storage.md`, `docs/tutorials/messaging/queues.md` and `docs/tutorials/databases/postgresql.md`. Each starts with its single-module install, shows connection details for the student's context, ends with a checkable result and explains exactly what survives uninstall.
- Under `lab/services/examples/csv-worker/`, use a small producer and worker with the same clients as the lessons. The producer uploads a CSV then sends its bucket, object key and stable job ID to the queue. The worker reads the object, writes the result in a transaction with a unique job ID, commits, then acknowledges the message. In a dedicated test queue, stop the worker after commit and before acknowledgment. Restart it and require redelivery of the same job ID. The worker must treat an existing result as complete and successfully delete the message with the receipt handle from this delivery. Stop the consumer, wait past the visibility timeout, then long-poll the queue and require a successful empty receive. Verify that exactly one result with the expected value exists for the job. For this exercise the producer explicitly sends the message; bucket notifications are unnecessary.
- Add the combined walkthrough to `docs/tutorials/README.md`. Update `README.md`, `docs/commands.md`, `docs/operations.md` and `docs/podman-rootless.md` with per-module commands, credential paths, endpoint contexts and troubleshooting. Include examples for a single lesson and the whole exercise.

## 7. Put the feature in CI and the annual VM acceptance run

- Run fast wrapper and help tests on every relevant PR and extend the existing Compose checks for both manifests. Add a dedicated Docker job to `.github/workflows/student-path.yml` for the live service tests. Include the helper, Compose files, configs, test script and CSV example in trigger paths. Capture stage logs on failure and keep runtime-project naming distinct from other jobs.
- Add a named stage to `scripts/verify-course-vm.py` immediately after its initial inventory check. Extend that preflight to detect installed modules **and retained but uninstalled service data** before any purge-capable tests. Fail closed on unreadable state. Exercise the three individual modules, their coexistence and the combined CSV workflow on the actual rootless VM as the student user. Check cleanup before handing control to the existing compute and Kubernetes suites. Do not purge data from an earlier course or interrupt an existing deployment.
- Extend `tests/test_verify_course_vm.py` for stage ordering, pre-existing retained data, a failed module probe, and nonzero status for an incomplete run. Update `docs/course-verification.md` with the added stage, expected evidence and measured time from the first live rehearsal.
- Run the following checks and attach results to the release record:

| Check | Expected evidence |
| --- | --- |
| `go test ./...` and `go test -tags docker . -run TestComposeConfiguration -v` | Existing commands still work; default and selected profile graphs match expectations. |
| `python3 -m unittest discover -s tests -p 'test_verify*.py'` | The annual runner protects existing state and propagates failure. |
| `bash tests/managed-services.sh all` on Docker and Podman | One report identifies the module, operation, runtime and result of each persistence, isolation and CSV assertion. |
| `python3 scripts/verify-course-vm.py` inside the IT-issued VM | All new and existing acceptance stages pass on the installed course version. Retain the report and image digests with the annual handover. |

The first release is complete when the three individual handouts work from a fresh installation, every allowed uninstall/reinstall cycle preserves data, and the annual acceptance run demonstrates these properties on the VM students will use.
