// Package real implements apiclient.Client against Apple's actual private
// iCloud web API (no official/documented API exists — see INTENT.md
// Constraints). The request/response shapes, headers, and endpoints here
// are ported from pyicloud (github.com/picklepete/pyicloud), an actively
// maintained open-source reference implementation, per plan.md Phase 3.
//
// IMPORTANT: unlike every other package in this module, this client's
// correctness cannot be verified by the fake-server test suite alone —
// it talks to real, undocumented Apple infrastructure that can change
// without notice. It has NOT yet been exercised against a real Apple ID
// (see plan.md Phase 3/9's manual smoke test, which is the first time
// this code should be run for real). Treat anything in here as
// best-effort until that smoke test passes, and expect to need fixes
// informed by real response bodies/headers.
package real

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"path"
	"strings"
	"time"

	"github.com/Stinger911/iSync/internal/apiclient"
)

const (
	authEndpoint  = "https://idmsa.apple.com/appleauth/auth"
	homeEndpoint  = "https://www.icloud.com"
	setupEndpoint = "https://setup.icloud.com/setup/ws/1"

	driveServiceID = "com.apple.CloudDocs"
)

// Client is the real apiclient.Client implementation. The zero value is
// not usable; construct with New.
type Client struct {
	httpClient *http.Client

	// Endpoint roots are fields (rather than package constants) so the
	// protocol can be exercised against an httptest server without any
	// real Apple traffic. New sets all three to Apple's production roots.
	authRoot  string
	setupRoot string
}

// New returns a Client with its own cookie jar and a reasonable timeout.
func New() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 60 * time.Second,
		},
		authRoot:  authEndpoint,
		setupRoot: setupEndpoint,
	}
}

var _ apiclient.Client = (*Client)(nil)

// state is the full set of tokens/identifiers needed to resume an
// authenticated session, persisted (via internal/credstore, as opaque
// bytes) in apiclient.Session.Raw.
type state struct {
	AppleID        string `json:"apple_id"`
	ClientID       string `json:"client_id"`
	SessionID      string `json:"session_id,omitempty"`
	SCNT           string `json:"scnt,omitempty"`
	SessionToken   string `json:"session_token,omitempty"`
	TrustToken     string `json:"trust_token,omitempty"`
	AccountCountry string `json:"account_country,omitempty"`
	DSID           string `json:"dsid,omitempty"`
	ServiceRoot    string `json:"service_root,omitempty"`  // drivews base URL
	DocumentRoot   string `json:"document_root,omitempty"` // docws base URL
}

func newClientID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "auth-" + hex.EncodeToString(b)
}

func loadState(session apiclient.Session) (*state, error) {
	var st state
	if len(session.Raw) == 0 {
		return nil, apiclient.ErrSessionExpired
	}
	if err := json.Unmarshal(session.Raw, &st); err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding session: %w", err)
	}
	return &st, nil
}

func (st *state) toSession() apiclient.Session {
	raw, _ := json.Marshal(st)
	return apiclient.Session{Raw: raw}
}

// doRequest issues an HTTP request, captures Apple's session-state
// response headers into st (mirroring pyicloud's HEADER_DATA mechanism),
// and maps common status codes to apiclient's typed errors.
func (c *Client) doRequest(ctx context.Context, st *state, method, url string, body []byte, extraHeaders map[string]string) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("apiclient/real: building request: %w", err)
	}

	req.Header.Set("Accept", "*/*")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", homeEndpoint)
	req.Header.Set("Referer", homeEndpoint+"/")
	if st.SCNT != "" {
		req.Header.Set("scnt", st.SCNT)
	}
	if st.SessionID != "" {
		req.Header.Set("X-Apple-ID-Session-Id", st.SessionID)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", apiclient.ErrTransient, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("apiclient/real: reading response: %w", err)
	}

	// Capture session-state-bearing response headers, as pyicloud's
	// HEADER_DATA table does.
	if v := resp.Header.Get("X-Apple-ID-Session-Id"); v != "" {
		st.SessionID = v
	}
	if v := resp.Header.Get("scnt"); v != "" {
		st.SCNT = v
	}
	if v := resp.Header.Get("X-Apple-Session-Token"); v != "" {
		st.SessionToken = v
	}
	if v := resp.Header.Get("X-Apple-TwoSV-Trust-Token"); v != "" {
		st.TrustToken = v
	}
	if v := resp.Header.Get("X-Apple-ID-Account-Country"); v != "" {
		st.AccountCountry = v
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return resp, respBody, apiclient.ErrRateLimited
	case 421, 450, 500, 502, 503, 504:
		return resp, respBody, apiclient.ErrTransient
	case http.StatusUnauthorized, http.StatusForbidden:
		return resp, respBody, apiclient.ErrInvalidCredentials
	case http.StatusNotFound:
		return resp, respBody, apiclient.ErrNotFound
	}

	return resp, respBody, nil
}

func authHeaders(st *state) map[string]string {
	stateID := "auth-" + st.ClientID
	return map[string]string{
		"X-Apple-OAuth-Client-Id":          st.ClientID,
		"X-Apple-OAuth-Client-Type":        "firstPartyAuth",
		"X-Apple-OAuth-Redirect-URI":       homeEndpoint,
		"X-Apple-OAuth-Require-Grant-Code": "true",
		"X-Apple-OAuth-Response-Mode":      "web_message",
		"X-Apple-OAuth-Response-Type":      "code",
		"X-Apple-OAuth-State":              stateID,
		"X-Apple-Widget-Key":               st.ClientID,
		"X-Apple-Frame-Id":                 stateID,
		"X-Requested-With":                 "XMLHttpRequest",
		"X-Apple-Mandate-Security-Upgrade": "0",
		"X-Apple-I-Require-UE":             "true",
	}
}

// Authenticate implements the current Apple web authentication flow: browser
// bootstrap, account federation, SRP-6a proof exchange, and (when needed)
// interactive 2FA. Apple removed the legacy plaintext /signin flow; do not
// send passwords to that endpoint.
func (c *Client) Authenticate(ctx context.Context, appleID, password string) (apiclient.Session, *apiclient.TwoFactorChallenge, error) {
	st := &state{AppleID: strings.ToLower(appleID), ClientID: newClientID()}
	if err := c.authStart(ctx, st); err != nil {
		return apiclient.Session{}, nil, err
	}
	if err := c.authFederate(ctx, st); err != nil {
		return apiclient.Session{}, nil, err
	}

	srp, err := newSRPClient()
	if err != nil {
		return apiclient.Session{}, nil, err
	}
	challenge, err := c.authSRPInit(ctx, st, srp.publicValue())
	if err != nil {
		return apiclient.Session{}, nil, err
	}
	key, err := derivePassword(password, challenge.salt, challenge.iterations, challenge.protocol)
	if err != nil {
		return apiclient.Session{}, nil, err
	}
	if err := srp.processChallenge([]byte(st.AppleID), key, challenge.salt, challenge.B); err != nil {
		return apiclient.Session{}, nil, err
	}

	needs2FA, err := c.authSRPComplete(ctx, st, challenge.c, srp.M1, srp.M2)
	if err != nil {
		return apiclient.Session{}, nil, err
	}
	if needs2FA {
		return apiclient.Session{}, &apiclient.TwoFactorChallenge{Submit: func(ctx context.Context, code string) (apiclient.Session, error) {
			return c.submitTwoFactorCode(ctx, st, code)
		}}, nil
	}

	data, err := c.accountLogin(ctx, st)
	if err != nil {
		return apiclient.Session{}, nil, err
	}
	if err := c.resolveDriveEndpoints(st, data); err != nil {
		return apiclient.Session{}, nil, err
	}
	return st.toSession(), nil, nil
}

func requiresTwoFactor(data map[string]any) bool {
	dsInfo, _ := data["dsInfo"].(map[string]any)
	hsaVersion, _ := dsInfo["hsaVersion"].(float64)
	challengeRequired, _ := data["hsaChallengeRequired"].(bool)
	trustedBrowser, _ := data["hsaTrustedBrowser"].(bool)
	return hsaVersion == 2 && (challengeRequired || !trustedBrowser)
}

func (c *Client) accountLogin(ctx context.Context, st *state) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{
		"accountCountryCode": st.AccountCountry,
		"dsWebAuthToken":     st.SessionToken,
		"extended_login":     true,
		"trustToken":         st.TrustToken,
	})
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: encoding accountLogin request: %w", err)
	}

	_, respBody, err := c.doRequest(ctx, st, http.MethodPost, c.setupRoot+"/accountLogin", body, nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: accountLogin: %w", err)
	}

	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding accountLogin response: %w", err)
	}

	if dsInfo, ok := data["dsInfo"].(map[string]any); ok {
		if dsid, ok := dsInfo["dsid"].(string); ok {
			st.DSID = dsid
		}
	}

	return data, nil
}

func (c *Client) submitTwoFactorCode(ctx context.Context, st *state, code string) (apiclient.Session, error) {
	body, err := json.Marshal(map[string]any{
		"securityCode": map[string]string{"code": code},
	})
	if err != nil {
		return apiclient.Session{}, fmt.Errorf("apiclient/real: encoding 2FA request: %w", err)
	}

	headers := authHeaders(st)
	headers["Accept"] = "application/json"

	_, _, err = c.doRequest(ctx, st, http.MethodPost, c.authRoot+"/verify/trusteddevice/securitycode", body, headers)
	if err != nil {
		return apiclient.Session{}, fmt.Errorf("apiclient/real: submitting 2FA code: %w", err)
	}

	// Trust this session so future logins skip 2FA (pyicloud's
	// trust_session: GET /2sv/trust, which yields a fresh
	// X-Apple-TwoSV-Trust-Token).
	if _, _, err := c.doRequest(ctx, st, http.MethodGet, c.authRoot+"/2sv/trust", nil, authHeaders(st)); err != nil {
		return apiclient.Session{}, fmt.Errorf("apiclient/real: trusting session: %w", err)
	}

	data, err := c.accountLogin(ctx, st)
	if err != nil {
		return apiclient.Session{}, err
	}
	if err := c.resolveDriveEndpoints(st, data); err != nil {
		return apiclient.Session{}, err
	}

	return st.toSession(), nil
}

func (c *Client) resolveDriveEndpoints(st *state, data map[string]any) error {
	webservices, ok := data["webservices"].(map[string]any)
	if !ok {
		return fmt.Errorf("apiclient/real: accountLogin response has no webservices")
	}
	drivews, ok := webservices["drivews"].(map[string]any)
	if !ok {
		return fmt.Errorf("apiclient/real: webservices missing drivews (iCloud Drive not enabled on this account?)")
	}
	docws, ok := webservices["docws"].(map[string]any)
	if !ok {
		return fmt.Errorf("apiclient/real: webservices missing docws (iCloud Drive not enabled on this account?)")
	}
	serviceRoot, _ := drivews["url"].(string)
	documentRoot, _ := docws["url"].(string)
	if serviceRoot == "" || documentRoot == "" {
		return fmt.Errorf("apiclient/real: drivews/docws webservice URLs were empty")
	}
	st.ServiceRoot = serviceRoot
	st.DocumentRoot = documentRoot
	return nil
}

// --- Drive operations ---

// driveNode is the subset of fields pyicloud's DriveNode relies on, as
// returned by retrieveItemDetailsInFolders.
type driveNode struct {
	DrivewsID    string      `json:"drivewsid"`
	Docwsid      string      `json:"docwsid"`
	Etag         string      `json:"etag"`
	Name         string      `json:"name"`
	Extension    string      `json:"extension"`
	Type         string      `json:"type"` // "FILE" or "FOLDER"
	Size         int64       `json:"size"`
	DateModified string      `json:"dateModified"`
	Items        []driveNode `json:"items"`
}

func (n driveNode) fullName() string {
	if n.Extension != "" {
		return n.Name + "." + n.Extension
	}
	return n.Name
}

func (n driveNode) isFolder() bool {
	return strings.EqualFold(n.Type, "FOLDER")
}

func (n driveNode) modifiedTime() time.Time {
	t, err := time.Parse(time.RFC3339, n.DateModified)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (c *Client) getNodeData(ctx context.Context, st *state, docwsid string) (driveNode, error) {
	reqBody, err := json.Marshal([]map[string]any{
		{"drivewsid": fmt.Sprintf("FOLDER::%s::%s", driveServiceID, docwsid), "partialData": false},
	})
	if err != nil {
		return driveNode{}, fmt.Errorf("apiclient/real: encoding retrieveItemDetailsInFolders request: %w", err)
	}

	_, respBody, err := c.doRequest(ctx, st, http.MethodPost, st.ServiceRoot+"/retrieveItemDetailsInFolders", reqBody, nil)
	if err != nil {
		return driveNode{}, fmt.Errorf("apiclient/real: retrieveItemDetailsInFolders: %w", err)
	}

	var nodes []driveNode
	if err := json.Unmarshal(respBody, &nodes); err != nil {
		return driveNode{}, fmt.Errorf("apiclient/real: decoding retrieveItemDetailsInFolders response: %w", err)
	}
	if len(nodes) == 0 {
		return driveNode{}, apiclient.ErrNotFound
	}
	return nodes[0], nil
}

// resolve walks p from the Drive root, returning the node at that path
// and its parent's docwsid (needed by Upload to address the containing
// folder). For the root itself, parentDocwsid is "".
func (c *Client) resolve(ctx context.Context, st *state, p string) (node driveNode, parentDocwsid string, err error) {
	clean := strings.Trim(path.Clean("/"+p), "/")

	root, err := c.getNodeData(ctx, st, "root")
	if err != nil {
		return driveNode{}, "", err
	}
	if clean == "" {
		return root, "", nil
	}

	parts := strings.Split(clean, "/")
	current := root
	parentID := "root"

	for i, part := range parts {
		var next *driveNode
		for j := range current.Items {
			if current.Items[j].fullName() == part {
				next = &current.Items[j]
				break
			}
		}
		if next == nil {
			return driveNode{}, "", apiclient.ErrNotFound
		}
		if i == len(parts)-1 {
			return *next, parentID, nil
		}
		if !next.isFolder() {
			return driveNode{}, "", apiclient.ErrNotFound
		}
		parentID = next.Docwsid
		current, err = c.getNodeData(ctx, st, next.Docwsid)
		if err != nil {
			return driveNode{}, "", err
		}
	}

	return current, parentID, nil
}

// ensureFolder resolves p, creating any missing intermediate folders
// (mirroring internal/apiclient/fake's implicit-parent-dir behavior), and
// returns the docwsid of the folder at p.
func (c *Client) ensureFolder(ctx context.Context, st *state, p string) (string, error) {
	clean := strings.Trim(path.Clean("/"+p), "/")
	if clean == "" {
		return "root", nil
	}

	parts := strings.Split(clean, "/")
	currentDocwsid := "root"
	current, err := c.getNodeData(ctx, st, "root")
	if err != nil {
		return "", err
	}

	for _, part := range parts {
		var next *driveNode
		for j := range current.Items {
			if current.Items[j].fullName() == part && current.Items[j].isFolder() {
				next = &current.Items[j]
				break
			}
		}
		if next == nil {
			if err := c.createFolder(ctx, st, currentDocwsid, part); err != nil {
				return "", err
			}
			current, err = c.getNodeData(ctx, st, currentDocwsid)
			if err != nil {
				return "", err
			}
			for j := range current.Items {
				if current.Items[j].fullName() == part && current.Items[j].isFolder() {
					next = &current.Items[j]
					break
				}
			}
			if next == nil {
				return "", fmt.Errorf("apiclient/real: folder %q not found immediately after creating it", part)
			}
		}
		currentDocwsid = next.Docwsid
		current, err = c.getNodeData(ctx, st, next.Docwsid)
		if err != nil {
			return "", err
		}
	}

	return currentDocwsid, nil
}

func (c *Client) createFolder(ctx context.Context, st *state, parentDocwsid, name string) error {
	body, err := json.Marshal(map[string]any{
		"destinationDrivewsId": fmt.Sprintf("FOLDER::%s::%s", driveServiceID, parentDocwsid),
		"folders": []map[string]any{
			{"clientId": st.ClientID, "name": name},
		},
	})
	if err != nil {
		return fmt.Errorf("apiclient/real: encoding createFolders request: %w", err)
	}

	_, _, err = c.doRequest(ctx, st, http.MethodPost, st.ServiceRoot+"/createFolders", body, nil)
	if err != nil {
		return fmt.Errorf("apiclient/real: createFolders: %w", err)
	}
	return nil
}

// List implements apiclient.Client.
func (c *Client) List(ctx context.Context, session apiclient.Session, p string) ([]apiclient.Item, error) {
	st, err := loadState(session)
	if err != nil {
		return nil, err
	}

	node, _, err := c.resolve(ctx, st, p)
	if err != nil {
		return nil, err
	}

	// If p resolved to a leaf item without its own children expanded
	// (resolve() returns the item as found in its parent's listing, which
	// already carries "items" only for folders fetched via getNodeData),
	// re-fetch by docwsid to get a full child listing.
	if node.isFolder() && node.Items == nil && node.Docwsid != "" {
		node, err = c.getNodeData(ctx, st, node.Docwsid)
		if err != nil {
			return nil, err
		}
	}
	if !node.isFolder() {
		return nil, apiclient.ErrNotFound
	}

	cleanParent := strings.TrimRight(path.Clean("/"+p), "/")
	items := make([]apiclient.Item, 0, len(node.Items))
	for _, child := range node.Items {
		items = append(items, apiclient.Item{
			Path:         path.Join(cleanParent, child.fullName()),
			IsDir:        child.isFolder(),
			Size:         child.Size,
			ModifiedTime: child.modifiedTime(),
			ETag:         child.Etag,
		})
	}
	return items, nil
}

// Fetch implements apiclient.Client.
func (c *Client) Fetch(ctx context.Context, session apiclient.Session, p string) (io.ReadCloser, error) {
	st, err := loadState(session)
	if err != nil {
		return nil, err
	}

	node, _, err := c.resolve(ctx, st, p)
	if err != nil {
		return nil, err
	}
	if node.isFolder() {
		return nil, fmt.Errorf("apiclient/real: %s is a folder", p)
	}
	if node.Size == 0 {
		// Apple's API returns 400 for 0-byte file downloads (noted in
		// pyicloud); short-circuit with an empty reader instead.
		return io.NopCloser(bytes.NewReader(nil)), nil
	}

	downloadURL := st.DocumentRoot + "/ws/" + driveServiceID + "/download/by_id?document_id=" + node.Docwsid
	_, respBody, err := c.doRequest(ctx, st, http.MethodGet, downloadURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: requesting download URL: %w", err)
	}

	var tokenResp struct {
		DataToken    *struct{ URL string } `json:"data_token"`
		PackageToken *struct{ URL string } `json:"package_token"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("apiclient/real: decoding download token response: %w", err)
	}

	fileURL := ""
	switch {
	case tokenResp.DataToken != nil:
		fileURL = tokenResp.DataToken.URL
	case tokenResp.PackageToken != nil:
		fileURL = tokenResp.PackageToken.URL
	default:
		return nil, fmt.Errorf("apiclient/real: download response had neither data_token nor package_token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("apiclient/real: building file download request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apiclient.ErrTransient, err)
	}
	return resp.Body, nil
}

// Upload implements apiclient.Client.
func (c *Client) Upload(ctx context.Context, session apiclient.Session, p string, content io.Reader, size int64) error {
	st, err := loadState(session)
	if err != nil {
		return err
	}

	dir, filename := path.Split(path.Clean("/" + p))
	parentDocwsid, err := c.ensureFolder(ctx, st, dir)
	if err != nil {
		return fmt.Errorf("apiclient/real: ensuring parent folder for %s: %w", p, err)
	}

	// Step 1: request an upload URL for this filename/size.
	initBody, err := json.Marshal(map[string]any{
		"filename":     filename,
		"type":         "FILE",
		"content_type": "",
		"size":         size,
	})
	if err != nil {
		return fmt.Errorf("apiclient/real: encoding upload/web request: %w", err)
	}
	_, initResp, err := c.doRequest(ctx, st, http.MethodPost, st.DocumentRoot+"/ws/"+driveServiceID+"/upload/web", initBody, map[string]string{"Content-Type": "text/plain"})
	if err != nil {
		return fmt.Errorf("apiclient/real: upload/web: %w", err)
	}

	var initResults []struct {
		DocumentID string `json:"document_id"`
		URL        string `json:"url"`
	}
	if err := json.Unmarshal(initResp, &initResults); err != nil || len(initResults) == 0 {
		return fmt.Errorf("apiclient/real: decoding upload/web response: %w", err)
	}
	documentID := initResults[0].DocumentID
	uploadURL := initResults[0].URL

	// Step 2: multipart-POST the file content to the returned URL.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(filename, filename)
	if err != nil {
		return fmt.Errorf("apiclient/real: building multipart upload body: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return fmt.Errorf("apiclient/real: reading upload content: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("apiclient/real: finalizing multipart upload body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return fmt.Errorf("apiclient/real: building file upload request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", apiclient.ErrTransient, err)
	}
	defer resp.Body.Close()
	uploadRespBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("apiclient/real: reading file upload response: %w", err)
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return apiclient.ErrFileTooLarge
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: file upload returned status %d", apiclient.ErrTransient, resp.StatusCode)
	}

	var uploadResp struct {
		SingleFile struct {
			Size              int64  `json:"size"`
			FileChecksum      string `json:"fileChecksum"`
			WrappingKey       string `json:"wrappingKey"`
			ReferenceChecksum string `json:"referenceChecksum"`
			Receipt           string `json:"receipt"`
		} `json:"singleFile"`
	}
	if err := json.Unmarshal(uploadRespBody, &uploadResp); err != nil {
		return fmt.Errorf("apiclient/real: decoding file upload response: %w", err)
	}

	// Step 3: commit the uploaded content as a file under parentDocwsid.
	now := time.Now().UnixMilli()
	commitData := map[string]any{
		"signature":           uploadResp.SingleFile.FileChecksum,
		"wrapping_key":        uploadResp.SingleFile.WrappingKey,
		"reference_signature": uploadResp.SingleFile.ReferenceChecksum,
		"size":                uploadResp.SingleFile.Size,
	}
	if uploadResp.SingleFile.Receipt != "" {
		commitData["receipt"] = uploadResp.SingleFile.Receipt
	}
	commitBody, err := json.Marshal(map[string]any{
		"data":              commitData,
		"command":           "add_file",
		"create_short_guid": true,
		"document_id":       documentID,
		"path": map[string]any{
			"starting_document_id": parentDocwsid,
			"path":                 filename,
		},
		"allow_conflict": true,
		"file_flags": map[string]any{
			"is_writable":   true,
			"is_executable": false,
			"is_hidden":     false,
		},
		"mtime": now,
		"btime": now,
	})
	if err != nil {
		return fmt.Errorf("apiclient/real: encoding update/documents request: %w", err)
	}

	if _, _, err := c.doRequest(ctx, st, http.MethodPost, st.DocumentRoot+"/ws/"+driveServiceID+"/update/documents", commitBody, map[string]string{"Content-Type": "text/plain"}); err != nil {
		return fmt.Errorf("apiclient/real: update/documents: %w", err)
	}

	return nil
}

// Delete implements apiclient.Client.
func (c *Client) Delete(ctx context.Context, session apiclient.Session, p string) error {
	st, err := loadState(session)
	if err != nil {
		return err
	}

	node, _, err := c.resolve(ctx, st, p)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"items": []map[string]any{
			{"drivewsid": node.DrivewsID, "etag": node.Etag, "clientId": st.ClientID},
		},
	})
	if err != nil {
		return fmt.Errorf("apiclient/real: encoding moveItemsToTrash request: %w", err)
	}

	if _, _, err := c.doRequest(ctx, st, http.MethodPost, st.ServiceRoot+"/moveItemsToTrash", body, nil); err != nil {
		return fmt.Errorf("apiclient/real: moveItemsToTrash: %w", err)
	}
	return nil
}
