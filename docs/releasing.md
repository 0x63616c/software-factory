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
   just release-check VERSION=v0.1.0
   ```

3. Create and push the exact tag:

   ```bash
   git tag -s v0.1.0 -m "Software Factory v0.1.0"
   git push origin v0.1.0
   ```

The tag-triggered release workflow verifies that the commit belongs to `main`,
re-runs `verify`, the real Temporal Session integration, and deterministic E2E,
then publishes all seven images with full-version and commit-SHA tags. It adds
OCI source/version/revision labels, SBOM and provenance attestations, an image
digest manifest, its `SHA256SUMS`, and a non-draft GitHub Release.

If any gate or image fails, the release is not published. Fix forward on `main`
and choose a new version; never move or reuse a published tag.

## Consumer verification

Inspect the GitHub Release and use the digest manifest as the only deployment
input. For example:

```bash
gh release view v0.1.0 -R 0x63616c/software-factory
docker buildx imagetools inspect ghcr.io/0x63616c/software-factory-worker:v0.1.0
```

The release tag is convenient discovery metadata. Production configuration must
use the corresponding `sha256:` digest, not the tag.
