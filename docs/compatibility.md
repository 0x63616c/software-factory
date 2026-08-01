# Compatibility policy

Software Factory begins at `v0.1.0`. While the major version is zero, its public
configuration, OpenAPI, database migration, Temporal history/build-ID, and
container entrypoint contracts may change between minor releases. Release notes
must identify required operator action and upgrade ordering.

Patch releases within one minor series must preserve those contracts unless a
security or data-integrity repair makes that impossible. Database migrations are
forward-only. Operators must back up Postgres before an upgrade and retain the
previous immutable image digests for rollback. A rollback is supported only
when the release notes state that its database and Temporal changes are backward
compatible.

`v1.0.0` is reserved for an explicit stable compatibility declaration. Until
then, the supported artifact surface is exactly the seven `linux/amd64` images
listed in `release/images.json`; source packages and internal Go APIs are not a
compatibility promise.
