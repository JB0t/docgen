package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIKeyIsSentAsBearer(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer srv.Close()

	t.Setenv("DOCGEN_LLM_API_KEY", "sk-test")
	c := New(srv.URL+"/v1", "m")
	resp, err := c.HTTP.Get(c.URL + "/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	t.Setenv("DOCGEN_LLM_API_KEY", "")
	c = New(srv.URL+"/v1", "m")
	resp, err = c.HTTP.Get(c.URL + "/models")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if len(got) != 2 || got[0] != "Bearer sk-test" || got[1] != "" {
		t.Fatalf("Authorization headers %q", got)
	}
}
