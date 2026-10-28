package real

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthenticateUsesSRPFlowAndPersistsEndpoints(t *testing.T) {
	var steps []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		steps = append(steps, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/authorize/signin", "/federate":
			w.WriteHeader(http.StatusOK)
		case "/signin/init":
			if r.Header.Get("X-Apple-Widget-Key") == "" {
				t.Error("SRP init omitted the Apple widget key")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iteration": 1,
				"salt":      base64.StdEncoding.EncodeToString([]byte("salt")),
				"protocol":  "s2k",
				"b":         base64.StdEncoding.EncodeToString(padToN(srpG)),
				"c":         "server-challenge",
			})
		case "/signin/complete":
			w.Header().Set("X-Apple-Session-Token", "session-token")
			w.Header().Set("X-Apple-ID-Account-Country", "US")
			w.WriteHeader(http.StatusOK)
		case "/accountLogin":
			_ = json.NewEncoder(w).Encode(map[string]any{"webservices": map[string]any{
				"drivews": map[string]any{"url": "https://drive.example.test"},
				"docws":   map[string]any{"url": "https://docs.example.test"},
			}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := New()
	client.httpClient = server.Client()
	client.authRoot = server.URL
	client.setupRoot = server.URL

	session, challenge, err := client.Authenticate(context.Background(), "User@Example.com", "password")
	if err != nil {
		t.Fatalf("Authenticate(): %v", err)
	}
	if challenge != nil {
		t.Fatal("Authenticate() returned a 2FA challenge for an HTTP 200 SRP completion")
	}
	st, err := loadState(session)
	if err != nil {
		t.Fatalf("loadState(): %v", err)
	}
	if st.AppleID != "user@example.com" || st.SessionToken != "session-token" || st.ServiceRoot != "https://drive.example.test" || st.DocumentRoot != "https://docs.example.test" {
		t.Errorf("persisted state = %#v; expected normalized ID, session token, and Drive endpoints", st)
	}
	want := []string{"GET /authorize/signin", "POST /federate", "POST /signin/init", "POST /signin/complete", "POST /accountLogin"}
	if len(steps) != len(want) {
		t.Fatalf("request count = %d, want %d; steps=%v", len(steps), len(want), steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %q, want %q", i, steps[i], want[i])
		}
	}
}
