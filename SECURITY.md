# Security policy

Do not open a public issue for a suspected vulnerability or include credentials,
tokens, private keys, webhook payload secrets, or production data in a report.
Use GitHub's private vulnerability reporting for this repository. Include the
affected version or image digest, impact, minimal reproduction, and any known
mitigation.

Only the latest released minor line receives security fixes while the project is
pre-1.0. A security advisory will state affected versions, patched versions, and
operator action. No response-time or embargo SLA is promised yet.

The main worker is the only model-credential holder. Run Workers receive
short-lived repository credentials and generation-scoped checkpoint
capabilities; tool workers are credential-free. Reports that cross one of those
boundaries are especially useful. See [`docs/system-map.md`](./docs/system-map.md)
and [`docs/configuration.md`](./docs/configuration.md).
