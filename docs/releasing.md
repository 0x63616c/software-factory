# Releasing Software Factory

Software Factory releases are stable root SemVer tags such as `v0.1.0`. A tag
is the single release intent: never assemble a release manually from a local
checkout.

The first release series publishes the seven existing application images for
`linux/amd64`: worker, run-worker, relay, API, blobs, codec, and console. It does
not publish a unified `sf` binary and does not create moving `latest`, `v0`, or
`v0.1` tags. Consumers must pin the digest recorded in the release manifest.

## Maintainer flow

1. Merge a green pull request to `main`.
2. From a clean checkout of that commit, run:

   ```bash
   just release
   ```

   Codex inspects the Conventional Commit subjects since the latest reachable
   SemVer tag, all local and remote tags, and GitHub Release records. It selects
   the next version and writes the release notes into the annotated local tag.
   The script validates the structured decision before creating the tag.

3. Inspect and push the exact tag reported by `just release`:

   ```bash
   git push origin v0.1.0
   ```

   Replace `v0.1.0` with the version selected in the preceding command. Pushing
   the tag starts the release workflow; it publishes the annotation as the
   GitHub Release notes. `just release` deliberately does not push.

The tag-triggered release workflow verifies that the commit belongs to `main`,
re-runs `verify`, the real Temporal Session integration, and deterministic E2E,
then builds all seven images under immutable commit-SHA tags. Only after every
build succeeds does it promote the complete set to full-version tags. It adds
OCI source/version/revision labels, SBOM and provenance attestations, an image
digest manifest, its `SHA256SUMS`, and a non-draft GitHub Release.

If any gate or image build fails, no SemVer image tag or GitHub Release is
published. A registry failure during final promotion can leave an incomplete
set of version tags but cannot create the GitHub Release; rerunning the workflow
is idempotent because every promotion names the already-built digest. Fix
forward on `main` and choose a new version after any content change; never move
or reuse a published tag.

## Consumer verification

Inspect the GitHub Release and use the digest manifest as the only deployment
input. For example:

```bash
gh release view v0.1.0 -R 0x63616c/software-factory
docker buildx imagetools inspect ghcr.io/0x63616c/software-factory-worker:v0.1.0
```

The release tag is convenient discovery metadata. Production configuration must
use the corresponding `sha256:` digest, not the tag.

## External canaries

The `External canary` workflow is manual, non-default, and targets the protected
`canary` GitHub Environment. It has two independent targets:

- `responses` makes one bounded request through the production direct Responses
  client and requires the exact expected answer.
- `github-exact-head-merge` creates a pull request in a disposable repository,
  squash-merges its exact observed head SHA, verifies the merge response, and
  deletes the temporary branch.

Configure Responses credentials as environment secrets and its model as
`CODEX_RESPONSES_MODEL`. Configure the GitHub target as
`CANARY_GITHUB_REPOSITORY` and use a `CANARY_GITHUB_TOKEN` secret scoped only to
that disposable repository. These canaries are deliberately excluded from pull
request CI, the release gate, and the deterministic E2E suite.
