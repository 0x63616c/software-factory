package github

import "net/http"

// installationTransport puts a currently-valid installation token on every
// request of the API plane.
//
// A transport rather than a per-call client clone, because go-github's copy()
// copies its rate-limit state by value: a client cloned per request throws away
// every rate-limit observation the SDK made, so its pre-emptive short-circuit
// never accumulates anything and never fires. One durable client keeps it.
type installationTransport struct {
	base http.RoundTripper
	auth *appAuth
}

// RoundTrip authenticates the request as the installation.
func (t *installationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.auth.currentToken(req.Context())
	if err != nil {
		return nil, err
	}

	// A RoundTripper must not modify the request it is given.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req)
}
