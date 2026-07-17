package api

import "context"

// ExchangeCLIToken exchanges a PKCE grant code for an API token. The endpoint is
// public (no bearer); secrets travel in the body only.
func (c *Client) ExchangeCLIToken(ctx context.Context, code, verifier, label string) (string, error) {
	body := map[string]string{"code": code, "code_verifier": verifier}
	if label != "" {
		body["label"] = label
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.postJSON(ctx, "/auth/cli/token", body, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// RevokeCurrentToken revokes the calling bearer token.
func (c *Client) RevokeCurrentToken(ctx context.Context) error {
	var out struct {
		Revoked bool `json:"revoked"`
	}
	return c.deleteJSON(ctx, "/api/v1/tokens/current", &out)
}

// CurrentToken introspects the calling bearer token.
func (c *Client) CurrentToken(ctx context.Context) (*TokenInfo, error) {
	var info TokenInfo
	if err := c.getJSON(ctx, "/api/v1/tokens/current", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ProbeReports validates a token against an older server without the
// introspection endpoint by requesting a single report.
func (c *Client) ProbeReports(ctx context.Context) error {
	return c.getJSON(ctx, "/api/v1/reports", pageQuery(1), nil)
}
