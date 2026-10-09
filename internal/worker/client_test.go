package worker

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/Khantdotcom/tsuzuku-runner/internal/workerapi"
)

const clientToken = "client-token"

func TestClientRegister(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	var got workerapi.RegisterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != workerapi.RegisterPath {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+clientToken {
			t.Errorf("Authorization = %q", auth)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{ID: id, Name: got.Name})
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/", clientToken)
	resp, err := client.Register(t.Context(), workerapi.RegisterRequest{Name: "w1", Slots: 2})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if resp.ID != id || resp.Name != "w1" {
		t.Errorf("response = %+v", resp)
	}
	if got.Name != "w1" || got.Slots != 2 {
		t.Errorf("server received %+v", got)
	}
}

func TestClientHeartbeat(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workerapi.HeartbeatPath(id) {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req workerapi.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CPUUsedPercent != 50 || req.MemoryUsedMB != 10 {
			t.Errorf("body = %+v, err %v", req, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, clientToken).Heartbeat(t.Context(), id, workerapi.HeartbeatRequest{CPUUsedPercent: 50, MemoryUsedMB: 10})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
}

func TestClientErrors(t *testing.T) {
	problemServer := func(status int, detail string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": detail})
		}))
	}

	t.Run("heartbeat 404 is ErrUnknownWorker", func(t *testing.T) {
		srv := problemServer(http.StatusNotFound, "worker is not registered")
		defer srv.Close()
		err := NewClient(srv.URL, clientToken).Heartbeat(t.Context(), uuid.Must(uuid.NewV7()), workerapi.HeartbeatRequest{})
		if !errors.Is(err, ErrUnknownWorker) {
			t.Fatalf("err = %v, want ErrUnknownWorker", err)
		}
	})

	t.Run("401 is an APIError with detail", func(t *testing.T) {
		srv := problemServer(http.StatusUnauthorized, "a valid worker token is required")
		defer srv.Close()
		_, err := NewClient(srv.URL, clientToken).Register(t.Context(), workerapi.RegisterRequest{})
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok {
			t.Fatalf("err = %v, want *APIError", err)
		}
		if apiErr.Status != http.StatusUnauthorized || apiErr.Detail != "a valid worker token is required" {
			t.Errorf("APIError = %+v", apiErr)
		}
	})

	t.Run("unreachable server", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		if _, err := NewClient(url, clientToken).Register(t.Context(), workerapi.RegisterRequest{}); err == nil {
			t.Fatal("expected an error")
		}
	})
}
