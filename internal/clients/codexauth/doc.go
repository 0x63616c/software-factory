// Package codexauth owns the model provider's durable OAuth credential. It is
// the only software-factory component that holds or presents a refresh token.
// Direct Responses calls receive only a derived access-token document through
// Source.ManagedCredentialFile; ticket sandboxes receive no provider material.
//
// # Refresh invariants
//
// A refresh token is rotating and effectively single-use. At most one actor
// may present a generation, and the rotated pair must be durably stored before
// another actor reads it. An unknown presentation outcome halts rather than
// risking repeated presentation and destroying a credential that may still be
// recoverable.
//
// The Kubernetes Secret's resourceVersion compare-and-swap is the lease and
// the credential update's linearization point:
//
//	Get   -> auth.json, refresh_state.json, version V0
//	CAS   -> attempt{holder: me}, V0 -> V1
//	OAuth -> present the refresh token only after the lease succeeds
//	CAS   -> rotated auth.json plus settled state, V1
//
// Contention before the OAuth request is safe to retry. A conflict or process
// loss after presentation is not. One bounded takeover is recorded explicitly;
// a second unresolved takeover is refused. The lease expiry comparison assumes
// the current single-node production cluster, where every eligible pod shares
// one kernel clock.
//
// # Operator recovery
//
// Fatal states require `codex login` followed by an out-of-band re-seed using
// scripts/seed-codex-auth.sh. Pulumi must never own the Secret contents because
// a later apply could overwrite a rotated credential with a spent value.
//
// # Derived model credential
//
// ManagedCredentialFile derives from the stored document rather than composing
// a smaller JSON object. It preserves provider-required fields and blanks, but
// does not remove, tokens.refresh_token. The direct Responses adapter extracts
// only access_token and account_id. The returned work.CredentialFile refuses
// JSON serialization and redacts logging, so it cannot cross Temporal history.
//
// # Leak boundary
//
// Secret values are never formatted, logged or returned from exported APIs.
// Refresh state contains only holder, time, serial and outcome. Provider error
// prose is discarded, redirects are refused, and refresh tokens travel only in
// the HTTPS request body. Test fixtures use synthetic unsigned JWTs.
package codexauth
