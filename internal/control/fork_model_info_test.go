package control

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/modelinfo"
	"gpt-load/internal/platform/config"
)

type modelInfoStub struct {
	calls int
}

func (stub *modelInfoStub) Read(context.Context) (modelinfo.Report, error) {
	stub.calls++
	return modelinfo.Report{
		ObservedAtMS: 1793188800000,
		Intelligence: modelinfo.Section[modelinfo.Intelligence]{Status: modelinfo.StatusUnavailable},
		Speed:        modelinfo.Section[modelinfo.Speed]{Status: modelinfo.StatusUnavailable},
		Quota:        modelinfo.Section[modelinfo.Quota]{Status: modelinfo.StatusUnavailable},
	}, nil
}

func TestModelInfoHTTPAuthenticationAndProjection(t *testing.T) {
	initControlI18n(t)
	fixture := newServiceFixture(t)
	key, err := fixture.service.CreateAccessKey(t.Context(), AccessKeyCreateRequest{Name: "model information reader"})
	if err != nil {
		t.Fatal(err)
	}
	reader := &modelInfoStub{}
	server := NewServer(&config.Config{AuthKey: "test-admin"}, fixture.service)
	server.SetModelInfoReader(reader)
	engine := gin.New()
	server.RegisterRoutes(engine)
	for _, token := range []string{"test-admin", key.Key} {
		recorder := performHomeRequest(engine, "/api/fork/model-info", token)
		if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("model info = %d %s", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data modelinfo.Report `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.ObservedAtMS == 0 || body.Data.Speed.Status != modelinfo.StatusUnavailable || body.Data.Speed.Data != nil {
			t.Fatalf("unexpected model info report %#v", body.Data)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("reader calls = %d", reader.calls)
	}
	for _, test := range []struct {
		path, token string
		status      int
	}{
		{"/api/fork/model-info", "", http.StatusUnauthorized},
		{"/api/fork/model-info?force=true", "test-admin", http.StatusBadRequest},
		{"/api/fork/model-info?url=https://example.com", "test-admin", http.StatusBadRequest},
		{"/api/fork/model-info?", "test-admin", http.StatusBadRequest},
	} {
		recorder := performHomeRequest(engine, test.path, test.token)
		if recorder.Code != test.status {
			t.Fatalf("%s = %d, want %d", test.path, recorder.Code, test.status)
		}
	}
	if reader.calls != 2 {
		t.Fatalf("rejected requests reached reader: %d", reader.calls)
	}
}
