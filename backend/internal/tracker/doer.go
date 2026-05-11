package tracker

import (
	"context"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
)

// doer is the upstream HTTP seam. The production implementation is
// surf-backed (newSurfDoer); tests inject a fake. Keeping this small
// keeps surf out of every other file in the package and out of all
// test files.
type doer interface {
	Do(ctx context.Context, url string) (status int, body []byte, err error)
}

// surfDoer drives a single, reusable surf client built with Chrome
// impersonation. A live probe confirmed Cloudflare clears on first try
// with no extra headers, proxy, or tuning.
type surfDoer struct {
	client *surf.Client
}

func newSurfDoer(_ time.Duration) doer {
	// surf does not currently take a request timeout in the builder.
	// We rely on the caller's context for cancellation. The argument is
	// kept on the signature so a future surf release can plug it in
	// without touching callers.
	c := surf.NewClient().
		Builder().
		Impersonate().
		Chrome().
		Build().
		Unwrap()
	return &surfDoer{client: c}
}

// Do performs a GET against url using surf with Chrome impersonation
// and returns (status, body bytes, err). No extra headers are set;
// Impersonate().Chrome() already produces a full Chrome header set.
func (s *surfDoer) Do(ctx context.Context, url string) (int, []byte, error) {
	resp := s.client.Get(g.String(url)).WithContext(ctx).Do()
	if resp.IsErr() {
		return 0, nil, resp.Err()
	}
	r := resp.Ok()
	bodyRes := r.Body.Bytes()
	if bodyRes.IsErr() {
		return int(r.StatusCode), nil, bodyRes.Err()
	}
	return int(r.StatusCode), []byte(bodyRes.Ok()), nil
}
