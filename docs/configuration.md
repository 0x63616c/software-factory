# Configuration and external capabilities

Software Factory owns its application behavior and images. An operator owns the
Kubernetes cluster, PostgreSQL, Temporal, ingress/authentication, observability,
secret storage, GitHub App installation, and immutable image-digest pins. The
project does not install those production dependencies in v0 releases.

Never place secrets in command arguments, image layers, workflow input, Temporal
history, or logs. Mount secret files or project Kubernetes Secret values only to
the process that consumes them.

## Main worker

The main worker requires:

- `SOFTWARE_FACTORY_DATABASE_URL`, `TEMPORAL_HOST_PORT`, and
  `TEMPORAL_NAMESPACE` for durable state and orchestration.
- `RUN_WORKER_NAMESPACE`, digest-pinned `RUN_WORKER_IMAGE`, and
  `RUN_WORKER_IMAGE_PULL_SECRET_NAME` for disposable repository workers.
- `CHECKPOINT_API_URL`, `BLOBS_URL`, `METRICS_ADDR`, and downward-API `POD_NAME`.
- `CODEX_RESPONSES_ENDPOINT` and `CODEX_AUTH_SECRET_NAME`. The named Secret
  contains the subscription-backed credential document and is read/rotated by
  the main worker only.
- `GITHUB_OWNER`, `GITHUB_REPO`, positive `GITHUB_APP_ID` and
  `GITHUB_APP_INSTALLATION_ID`, plus `GITHUB_APP_PRIVATE_KEY_PEM_FILE`. The file
  contains the base64 text of the App PEM, not a raw PEM.

Optional worker values are `LOG_LEVEL` (`debug`, `info`, `warn`, or `error`),
`RUN_WORKER_CPU_REQUEST` (default `2`), and `RUN_WORKER_MEMORY_LIMIT` (default
`8Gi`). Dispatcher concurrency/model policy is durable workflow configuration,
not a second set of environment variables.

The worker ServiceAccount needs narrowly scoped access to create/delete Run
Worker pods and their generation Secrets in `RUN_WORKER_NAMESPACE`, read/update
only `CODEX_AUTH_SECRET_NAME`, and use the Kubernetes coordination lease needed
for single-writer credential refresh. It does not need cluster-admin.

## API

The API requires `API_ADDR`, `METRICS_ADDR`, `TEMPORAL_HOST_PORT`,
`TEMPORAL_NAMESPACE`, `CLOUDFLARE_ACCESS_TEAM_DOMAIN`,
`CLOUDFLARE_ACCESS_AUD`, `SOFTWARE_FACTORY_API__WORKER_BEARER_TOKEN`,
`SOFTWARE_FACTORY_API__RUN_WORKER_BEARER_TOKEN`, and
`GITHUB_BOT_APP__WEBHOOK_SECRET`.

Set `SOFTWARE_FACTORY_DATABASE_URL`, or set all four composition fields:
`SOFTWARE_FACTORY_DATABASE_USER`, `SOFTWARE_FACTORY_DATABASE_PASSWORD`,
`SOFTWARE_FACTORY_DATABASE_HOST`, and `SOFTWARE_FACTORY_DATABASE_NAME`.

## Supporting services

- Blobs: `BLOBS_ROOT` and `LISTEN_ADDR`; optional `LOG_LEVEL`.
- Codec: `LISTEN_ADDR`, `BLOBS_URL`, and a comma-separated allowlist in
  `CODEC_CORS_ORIGINS`; optional `LOG_LEVEL`.
- Relay: `LISTEN_ADDR`, `METRICS_ADDR`, `GITHUB_BOT_APP__WEBHOOK_SECRET`, and
  `RELAY_TARGETS`, a JSON list of unique `{name,url}` HTTP(S) targets.
- Console: the image serves static content and proxies `/api` using
  `web/nginx.conf`; the operator supplies ingress and access policy.

## Managed Run Workers

Operators do not set per-Run identity, branch, queue, repository, or checkpoint
values. The main worker derives and injects them, projects short-lived GitHub and
checkpoint capabilities, and deletes the worker after terminal completion.
`RUN_ID`, `RUN_WORKER_ID`, `RUN_WORKER_GENERATION`, `RUN_WORKER_BRANCH`,
`RUN_WORKER_TASK_QUEUE`, and `TOOL_WORKER_TASK_QUEUE` are internal protocol
fields. Treating them as deployment configuration breaks generation fencing.

## Secret capabilities

The minimum external secret set is:

- GitHub App private key and webhook HMAC secret.
- API worker and Run Worker bearer tokens, with different values.
- PostgreSQL credentials when not embedded in a mounted URL.
- Model credential document in the exact named Kubernetes Secret.
- GHCR pull credential when release images are private.

GitHub credentials projected to Run Workers must be installation-scoped and
short-lived. Checkpoint capabilities are generated per Attempt/generation and
must never be reused as general API bearer tokens.
