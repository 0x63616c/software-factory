# Images

Seven release images from one standalone source tree, all amd64-only. The
home-server Talos node is the only deploy target and it is x86.

| path | purpose |
|---|---|
| `worker/` | activated Temporal control and main worker; distroless static, nonroot uid 65532, no shell |
| `run-worker/` | per-Run repository and tool workers plus the repository toolchain; Debian slim, uid 1000 |
| `relay/` | stateless GitHub webhook fan-out edge service; distroless static, nonroot uid 65532 |
| `api/` | authenticated Ticket, Run, Step, Attempt, and transcript API |
| `blobs/` | content-addressed transcript and conversation blob service |
| `codec/` | Temporal payload codec service |
| `../web/` | static console and same-origin `/api/*` proxy |

CI builds every image when the shared path filter matches and production pins
each deployed image by digest. The Run Worker image is not a static Deployment:
its digest is the worker Deployment's `RUN_WORKER_IMAGE` value and each active
Run generation creates a pod from that exact value.

## What the Run Worker ships, and why

The image contains both `/usr/local/bin/run-worker` and
`/usr/local/bin/tool-worker`. A Run Worker pod starts them as separate
containers on separate generation-scoped Temporal queues. They share only the
`/work` `emptyDir` checkout.

- **`git` and `gh`** support fixed repository and GitHub activities. Their
  projected capability files mount only into the credentialed `run-worker`
  container and are reopened for each command so rotation takes effect.
- **`bun`/`bunx`, Node, and the Go toolchain** let the credential-free tool
  worker build and test both halves of this repository. Bun and Go match the
  versions used by the repository and CI.
- **`gcc` and `libc6-dev`** allow `CGO_ENABLED=1 go test -race` to link, matching
  the authoritative CI gate.
- **`golangci-lint`** is pinned to the same version as CI so the local wall and
  CI wall agree.
- **`sqlc`** is checksum-pinned so an Agent may regenerate committed query code
  after SQL changes.
- **Playwright Chromium** carries checksum-pinned headed and headless browser
  payloads plus ffmpeg and Xvfb support for browser tests and screenshots.
- **A shell and core command-line tools** are present because a typed tool call
  may intentionally execute repository commands. The main worker never sends
  an implicit shell string.

Formatters beyond those toolchains are not preinstalled. CI remains the
authoritative wall.

The repository and `node_modules` are not baked into the image. A fixed
repository activity clones the requested commit into `/work/repo` with a
short-lived GitHub App capability. The checkout is therefore current at Run
time and the image never captures a stale lockfile installation.

## Credential boundary

The credentialed `run-worker` registers only fixed typed repository, GitHub,
CI, and checkpoint activities. It cannot execute model-selected argv. The
credential-free `tool-worker` registers only the typed tool activity. It has no
projected Secret, provider credential, or Kubernetes service-account token.

Both containers run as uid 1000 without privilege escalation or added Linux
capabilities. The shared `/work` volume is writable through `fsGroup: 1000`;
the repository capability mounts exist only in the repository container.
Repository tools are rooted at `/work/repo` and reject path traversal or a
working directory outside that checkout.

Direct Responses model calls, prompt rendering, lifecycle evidence, and final
transcript persistence stay on the main worker. The model may edit, test, and
commit through typed tools, but only fixed repository activities can clone,
push, synchronize a pull request, mark it ready, or merge it. The main worker
creates and deletes Run Worker pods through the Kubernetes API and never uses
`pods/exec` or remote file transfer.

## Verifying

```sh
docker build --platform linux/amd64 \
  -f images/run-worker/Dockerfile \
  -t sf-run-worker:local .
images/run-worker/smoke.sh sf-run-worker:local
```

The smoke test mounts a temporary filesystem over `/work`, as Kubernetes does
for the pod `emptyDir`, and checks both worker binaries, the uid/workspace
contract, toolchains, browser payload, and projected credential readers. The
Go pod-spec tests separately prove that credentials mount only into
`run-worker` and that `tool-worker` receives neither credential files nor a
service-account token.
