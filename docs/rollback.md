# Rollback

Blob keeps Nomad job versions for each deploy. `blob rollback` promotes a previous release image back to the live job while keeping Blob's rendered job file and projection metadata in sync.

```sh
blob releases my-app
blob rollback my-app 2
```

The command asks for confirmation unless `--yes` is passed.

```sh
blob rollback my-app 2 --yes
```

Rollback is image-focused. It preserves the currently accepted job shape on disk: domain routing, service bindings, resources, replicas, volumes, sidecars, and isolation settings. That matches the most common failure case, where a new image is bad and the platform should put the previous image back without discarding operator changes made after the older release.

For HTTP services and static sites, Blob waits for the job to become running again. Deploy plugin hooks also run with `BLOB_HOOK=pre` before the rollback submit and `BLOB_HOOK=post` after the workload is ready.

## Doctor and drift

Do not use `nomad job revert` directly unless you are repairing the platform by hand. Direct Nomad changes leave `/srv/blob/jobs/<app>.nomad`, `/srv/blob/jobs/<app>.meta.json`, and the live Nomad job out of agreement, which `blob doctor` reports as projection drift.

`blob rollback` writes a new accepted projection hash into all three places, so a clean rollback remains clean under `blob doctor`.
