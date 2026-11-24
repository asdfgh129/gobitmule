package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeSearchProvider struct {
	params  ED2KSearchParams
	started int
	stopped int
	status  ED2KSearchStatus
}

func (f *fakeSearchProvider) Start(_ context.Context, params ED2KSearchParams) (ED2KSearchStatus, error) {
	f.params = params
	f.started++
	f.status.Snapshot.State = ED2KSearchRunning
	f.status.Snapshot.Params = params
	return f.status, nil
}

func (f *fakeSearchProvider) Snapshot(_ context.Context) (ED2KSearchStatus, error) {
	return f.status, nil
}

func (f *fakeSearchProvider) Stop(_ context.Context) (ED2KSearchStatus, error) {
	f.stopped++
	f.status.Snapshot.State = ED2KSearchStopped
	return f.status, nil
}

func TestED2KSearchDisabled(t *testing.T) {
	srv := httptest.NewServer(New(Options{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/ed2k/search")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got ED2KSearchStatus
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Available || got.Snapshot.State != ED2KSearchIdle {
		t.Fatalf("disabled status = %+v, want unavailable/IDLE", got)
	}
	body := bytes.NewBufferString(`{"query":"test"}`)
	r, err := http.Post(srv.URL+"/api/v1/ed2k/search", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("POST with disabled provider: status=%d, want 503", r.StatusCode)
	}
}

func TestED2KSearchStartValidateAndStop(t *testing.T) {
	fake := &fakeSearchProvider{status: ED2KSearchStatus{
		Available: true,
		Snapshot:  ED2KSearchSnapshot{State: ED2KSearchIdle, Results: []ED2KSearchResult{}},
	}}
	srv := httptest.NewServer(New(Options{Search: fake}))
	defer srv.Close()

	invalid := bytes.NewBufferString(`{"query":"   ","scope":"bogus"}`)
	r, err := http.Post(srv.URL+"/api/v1/ed2k/search", "application/json", invalid)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid params: status=%d, want 400", r.StatusCode)
	}
	if fake.started != 0 {
		t.Fatal("invalid params reached the provider")
	}

	valid := bytes.NewBufferString(`{"query":"ubuntu","scope":"kad","minSize":1024,"minSources":2}`)
	r2, err := http.Post(srv.URL+"/api/v1/ed2k/search", "application/json", valid)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("valid params: status=%d, want 200", r2.StatusCode)
	}
	if fake.started != 1 || fake.params.Query != "ubuntu" || fake.params.Scope != ED2KScopeKAD ||
		fake.params.MinSize != 1024 || fake.params.MinSources != 2 {
		t.Fatalf("provider params = %+v (started=%d)", fake.params, fake.started)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/ed2k/search", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || fake.stopped != 1 {
		t.Fatalf("stop: status=%d stopped=%d", resp.StatusCode, fake.stopped)
	}
}

func TestValidateED2KSearchParams(t *testing.T) {
	cases := []struct {
		name string
		p    ED2KSearchParams
		ok   bool
	}{
		{"default scope", ED2KSearchParams{Query: "abc"}, true},
		{"negative size", ED2KSearchParams{Query: "abc", MinSize: -1}, false},
		{"reversed range", ED2KSearchParams{Query: "abc", MinSize: 20, MaxSize: 10}, false},
		{"negative sources", ED2KSearchParams{Query: "abc", MinSources: -1}, false},
		{"long query", ED2KSearchParams{Query: string(make([]byte, 201))}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateED2KSearchParams(tc.p)
			if (err == nil) != tc.ok {
				t.Fatalf("validate() error=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}
