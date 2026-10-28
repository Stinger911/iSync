package real

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/Stinger911/iSync/internal/apiclient"
)

type srpChallenge struct {
	iterations int
	salt       []byte
	B          []byte
	c          string
	protocol   string
}

func (c *Client) authStart(ctx context.Context, st *state) error {
	q := url.Values{
		"frame_id":      {"auth-" + st.ClientID},
		"language":      {"en_US"},
		"skVersion":     {"7"},
		"iframeId":      {"auth-" + st.ClientID},
		"client_id":     {st.ClientID},
		"redirect_uri":  {homeEndpoint},
		"response_type": {"code"},
		"response_mode": {"web_message"},
		"state":         {"auth-" + st.ClientID},
		"authVersion":   {"latest"},
	}
	resp, _, err := c.doRequest(ctx, st, http.MethodGet, c.authRoot+"/authorize/signin?"+q.Encode(), nil, authHeaders(st))
	if err != nil {
		return fmt.Errorf("apiclient/real: auth bootstrap: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apiclient/real: auth bootstrap returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) authFederate(ctx context.Context, st *state) error {
	body, err := json.Marshal(map[string]any{"accountName": st.AppleID, "rememberMe": true})
	if err != nil {
		return fmt.Errorf("apiclient/real: encoding federation request: %w", err)
	}
	resp, _, err := c.doRequest(ctx, st, http.MethodPost, c.authRoot+"/federate?isRememberMeEnabled=true", body, authHeaders(st))
	if err != nil {
		return fmt.Errorf("apiclient/real: federating account: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("apiclient/real: account federation returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) authSRPInit(ctx context.Context, st *state, publicValue []byte) (*srpChallenge, error) {
	body, err := json.Marshal(map[string]any{
		"a":           base64.StdEncoding.EncodeToString(publicValue),
		"accountName": st.AppleID,
		"protocols":   []string{"s2k", "s2k_fo"},
	})
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: encoding SRP init: %w", err)
	}
	_, raw, err := c.doRequest(ctx, st, http.MethodPost, c.authRoot+"/signin/init", body, authHeaders(st))
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: SRP init: %w", err)
	}
	var response struct {
		Iteration int    `json:"iteration"`
		Salt      string `json:"salt"`
		Protocol  string `json:"protocol"`
		B         string `json:"b"`
		C         string `json:"c"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding SRP init: %w", err)
	}
	salt, err := base64.StdEncoding.DecodeString(response.Salt)
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding SRP salt: %w", err)
	}
	B, err := base64.StdEncoding.DecodeString(response.B)
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding SRP server public value: %w", err)
	}
	if response.Iteration <= 0 || response.Protocol == "" || response.C == "" {
		return nil, fmt.Errorf("apiclient/real: incomplete SRP init response")
	}
	return &srpChallenge{iterations: response.Iteration, salt: salt, B: B, c: response.C, protocol: response.Protocol}, nil
}

// authSRPComplete returns needs2FA rather than treating Apple's 409 response
// as an error: it is the expected signal after a valid password proof for a
// two-factor-protected account.
func (c *Client) authSRPComplete(ctx context.Context, st *state, serverChallenge string, M1, M2 []byte) (needs2FA bool, err error) {
	trustTokens := []string{}
	if st.TrustToken != "" {
		trustTokens = append(trustTokens, st.TrustToken)
	}
	body, err := json.Marshal(map[string]any{
		"accountName": st.AppleID,
		"m1":          base64.StdEncoding.EncodeToString(M1),
		"m2":          base64.StdEncoding.EncodeToString(M2),
		"c":           serverChallenge,
		"rememberMe":  true,
		"trustTokens": trustTokens,
	})
	if err != nil {
		return false, fmt.Errorf("apiclient/real: encoding SRP completion: %w", err)
	}
	resp, _, err := c.doRequest(ctx, st, http.MethodPost, c.authRoot+"/signin/complete?isRememberMeEnabled=true", body, authHeaders(st))
	if err != nil {
		return false, fmt.Errorf("apiclient/real: SRP completion: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return false, nil
	case http.StatusConflict:
		return true, nil
	case http.StatusForbidden:
		return false, apiclient.ErrInvalidCredentials
	default:
		return false, fmt.Errorf("apiclient/real: SRP completion returned HTTP %d", resp.StatusCode)
	}
}
