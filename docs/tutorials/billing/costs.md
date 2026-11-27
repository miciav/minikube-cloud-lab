# Usage and costs

Start the cloud using the [course setup](../../../README.md#prepare-the-environment) with no cluster or instances, or note what already exists: it accrues too. Prices are fictitious: `lab-small` costs 0.10 EUR per hour, `lab-medium` 0.40 EUR per hour and storage 0.10 EUR per GiB per month. The portal **Costs** section shows the same figures for Since reset, Last hour and Last 30 minutes.

```sh
cloud machine-types list
cloud billing
```

The output has a `Since ... (reset ...)` header, one row per resource (`KIND NAME TYPE/SIZE STATUS HOURS COST_EUR`) and a final `TOTAL` line.

## After creating a cluster

```sh
cloud clusters create demo --machine-type lab-small
cloud billing
```

The control plane and each worker appear as `machine` rows with `running` status. Run `cloud billing` again a few minutes later: HOURS and COST_EUR grow while the cluster exists.

## After autoscaling

Run the [Pod-to-machine exercise](../autoscaling/kubernetes-nodes.md) or `cloud clusters resize demo --nodes 3`, then check:

```sh
cloud billing
```

More workers mean more rows and a higher cost per hour. When the autoscaler removes a worker its row stops growing but stays in the total, so the cost of the period is kept.

## After stopping an instance

```sh
cloud instances create vm1
cloud disks create data --size 1Gi
cloud billing
cloud instances stop vm1
cloud billing
```

The `instance` row changes to `stopped` and stops accruing compute. The `disk` row and any Kubernetes `volume` rows keep costing until deleted, even if retained or detached.

## Compare periods

```sh
cloud billing --since 30m
cloud billing
```

`--since` takes a duration such as `30m` or `1h`, or an RFC3339 time. Only usage inside the window is counted, so the first total is lower than the second. A time before the last reset is clamped to the reset; a future or malformed value is rejected.

## Cleanup

```sh
cloud instances delete vm1
cloud disks delete data
cloud clusters delete demo
cloud billing
```

Deleted resources stop accruing, but their cost stays in the total. To restart the count run `labcompose down -v`, which removes the billing volume (and all other lab data).
