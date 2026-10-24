# Control and data API

The Cluster tab (create / start / stop / deploy / scale / restart / checks)
and the Data tab (stamps, upload, download) use these endpoints. They exist
only in live mode; in sim and replay mode `GET /api/control/status` returns
`enabled: false` with a reason, and every other endpoint returns 409.

Every POST needs `Content-Type: application/json` (except the upload, see
below), a same-origin `Origin` header, and `"confirm": true` in the body.
Errors are `{"error": "..."}` with a 4xx/5xx status.

Nothing here reads or returns the beekeeper wallet key, the kubeconfig or
any Authorization header. Job output is scrubbed of 64-hex-character
strings that equal the configured wallet key.

## Status

`GET /api/control/status`

```json
{
  "enabled": true,
  "disabledReason": "",
  "settings": Settings,
  "tools": [{ "name": "docker", "path": "/usr/local/bin/docker", "ok": true, "hint": "" }],
  "docker": { "running": true, "error": "" },
  "cluster": {
    "name": "bee", "kubeContext": "k3d-bee", "namespace": "local",
    "exists": true, "running": true, "registryRunning": true,
    "registryPort": "5002", "localImage": "k3d-registry.localhost:5002/ethersphere/bee:latest", "error": ""
  },
  "geth": { "installed": true, "ready": true },
  "nodes": [NodeStatus],
  "deployed": { "groups": { "bootnode": 1, "bee": 3, "light": 2 }, "image": "ethersphere/bee:2.8.0" },
  "profiles": [Profile],
  "checks": ["ci-pushsync-chunks", "ci-retrieval", "..."],
  "job": Job | null
}
```

`tools` lists docker, k3d, kubectl, helm, beekeeper, jq, envsubst, make.
`hint` says how to install a missing one.

`NodeStatus`:

```json
{
  "name": "bee-1", "group": "bee", "replicas": 1, "ready": 1,
  "image": "ethersphere/bee:2.8.0", "pod": "bee-1-0",
  "phase": "Running", "restarts": 3, "startedAt": "2026-10-24T10:00:00Z",
  "reason": ""
}
```

`phase` is the pod phase, `"Stopped"` when replicas is 0, or `"Missing"`
when there is no pod. `reason` is a waiting reason such as
`CrashLoopBackOff` or `ImagePullBackOff`.

`Profile` is a beekeeper cluster from the config dir that can run on the
local k3d cluster (namespace `local`):

```json
{ "name": "local-dns", "groups": [{ "name": "bee", "mode": "node", "count": 3 }, { "name": "light", "mode": "node", "count": 2 }, { "name": "bootnode", "mode": "bootnode", "count": 1 }], "image": "k3d-registry.localhost:5000/ethersphere/bee:latest" }
```

## Settings

`GET /api/control/settings` returns `Settings`; `PUT /api/control/settings`
takes a partial `Settings` (plus `"confirm": true`) and returns the saved
settings. They are stored in the user config dir
(`~/Library/Application Support/bee-scope/settings.json` on macOS).

```json
{
  "beelocalDir": "/Users/me/swarm/beelocal",
  "beekeeperConfigDir": "/Users/me/swarm/beekeeper/config",
  "beeRepoDir": "/Users/me/swarm/bee",
  "beekeeper": "/Users/me/go/bin/beekeeper",
  "profile": "local-dns",
  "counts": { "bee": 3, "light": 2 },
  "image": "ethersphere/bee:latest",
  "detected": { "beelocalDir": true, "beekeeperConfigDir": true, "beeRepoDir": true, "beekeeper": true }
}
```

Empty fields are auto-detected at startup; `detected` says which ones were.

`POST /api/quit` `{"confirm": true}` stops bee-scope (the cluster keeps running). Available in every mode.

## Jobs

Long operations run as jobs, one at a time. Starting a job while one runs
returns 409.

`POST /api/control/actions/{name}` → `202 {"job": Job}`

| name | body (besides `confirm`) | what it runs |
|---|---|---|
| `docker-start` | | `open -a Docker`, then waits for `docker info` |
| `cluster-create` | | beelocal's k8s-local steps: registry, `k3d cluster create` from beelocal `config/k3d.yaml`, helm repo, traefik wait, geth-swap install |
| `cluster-start` | | `docker start k3d-registry.localhost`, `k3d cluster start bee` |
| `cluster-stop` | | `k3d cluster stop bee`, `docker stop k3d-registry.localhost` |
| `cluster-delete` | | `k3d cluster delete bee`, `k3d registry delete k3d-registry.localhost` |
| `bee-deploy` | `profile`, `counts` `{group: n}`, `image` | generates a `bee-scope` beekeeper cluster from the profile, `beekeeper create bee-cluster`, then removes nodes above the requested counts |
| `bee-remove` | | deletes every Bee node (statefulsets, services, ingressroutes, secrets, configmaps, PVCs, service accounts labeled `app.kubernetes.io/name=bee`); geth stays |
| `node-restart` | `node` | deletes the node's pod |
| `node-stop` | `node` | scales the node's statefulset to 0 |
| `node-start` | `node` | scales the node's statefulset to 1 |
| `restart-all` | | deletes every Bee pod |
| `image-build` | `tag` (default `latest`) | in the bee repo: `make docker-build PLATFORM=linux/<arch>`, then pushes `localhost:<registryPort>/ethersphere/bee:<tag>`; nodes use `k3d-registry.localhost:<registryPort>/ethersphere/bee:<tag>` (see `cluster.localImage`) |
| `check-run` | `checks` `[string]`, `timeout` (default `15m`) | `beekeeper check --cluster-name bee-scope --checks …` |

`Job`:

```json
{
  "id": "j7", "action": "bee-deploy", "title": "Deploy 1 bootnode, 3 bee, 2 light",
  "state": "running", "startedAt": "…", "endedAt": "…", "error": "",
  "steps": [{ "title": "Generate beekeeper config", "state": "done" }, { "title": "beekeeper create bee-cluster", "state": "running" }],
  "lines": 132
}
```

`state`: `running`, `succeeded`, `failed`, `canceled`. Step `state`:
`pending`, `running`, `done`, `failed`, `skipped`.

- `GET /api/control/jobs` → `[Job]`, newest first (last 20).
- `GET /api/control/jobs/{id}/log?after=N` → `{"job": Job, "lines": [{"n": 1, "t": "…", "stream": "out"|"err"|"info", "text": "…"}], "next": N}`. Poll with `after=next`.
- `POST /api/control/jobs/{id}/cancel` → `{"job": Job}`.

## Data

All data endpoints talk to one node through the kube service proxy.
`{node}` is a discovered node name (`bee-1`).

`GET /api/data/{node}/get?path=/stamps` is a read-only passthrough for
`/stamps`, `/stamps/{id}`, `/stamps/{id}/buckets`, `/wallet`, `/addresses`,
`/chainstate`, `/chequebook/balance`, `/tags`, `/tags/{uid}`, `/pins`,
`/readiness`, `/health`. It returns Bee's JSON body and status code.

`POST /api/data/{node}/stamps` `{"amount": "100000000", "depth": 20, "label": "", "immutable": false, "confirm": true}`
→ Bee `POST /stamps/{amount}/{depth}` → `{"batchID": "…", "txHash": "…"}`.
Can take a minute (Bee waits for the transaction).

`POST /api/data/{node}/stamps/{id}/topup` `{"amount": "…", "confirm": true}` → `PATCH /stamps/topup/{id}/{amount}`.

`POST /api/data/{node}/stamps/{id}/dilute` `{"depth": 21, "confirm": true}` → `PATCH /stamps/dilute/{id}/{depth}`.

`POST /api/data/{node}/upload?batch=<id>&name=<file name>&pin=1&encrypt=0&deferred=1&redundancy=0`
- body: the raw file bytes; `Content-Type`: the file's type.
- requires the header `X-Bee-Scope: upload` (CSRF guard instead of the JSON rule) and a same-origin `Origin`.
- → Bee `POST /bzz?name=…` → `{"reference": "…", "tagUid": 12, "upload": Upload}`.

`GET /api/data/uploads` → `[Upload]`, newest first (this session):

```json
{ "id": "u3", "node": "bee-1", "name": "photo.jpg", "size": 123456, "contentType": "image/jpeg", "batch": "…", "reference": "…", "tagUid": 12, "at": "…", "encrypt": false, "pin": true }
```

`GET /api/data/{node}/download/{reference}?path=<optional file path>&name=<save-as>`
streams Bee's `GET /bzz/{reference}/{path}` back with `Content-Disposition: attachment`.
