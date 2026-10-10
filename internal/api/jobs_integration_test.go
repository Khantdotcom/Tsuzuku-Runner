//go:build integration

package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testStale = 15 * time.Second

type jobBody struct {
	ID          uuid.UUID `json:"id"`
	Number      int64     `json:"number"`
	State       string    `json:"state"`
	StartedAt   *string   `json:"started_at"`
	FinishedAt  *string   `json:"finished_at"`
	Workload    workloadBody
	Transitions []struct {
		From   *string `json:"from"`
		To     string  `json:"to"`
		Actor  string  `json:"actor"`
		Reason string  `json:"reason"`
	} `json:"transitions"`
}

type workloadBody struct {
	ID   uuid.UUID `json:"id"`
	Spec struct {
		Repository string `json:"repository"`
		Command    string `json:"command"`
		Runtime    struct {
			Image string `json:"image"`
		} `json:"runtime"`
	} `json:"spec"`
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) decode(t *testing.T, out any) {
	t.Helper()
	if err := json.Unmarshal(r.body, out); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func submitWorkload(t *testing.T, baseURL, body, key string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/api/v1/workloads", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func workloadJSON(command string) string {
	return fmt.Sprintf(`{"repository":"https://github.com/example/app","revision":"main","command":%q}`, command)
}

func TestSubmitAndReadJob(t *testing.T) {
	srv := newServer(t, testStale)

	resp := submitWorkload(t, srv.URL, workloadJSON("go test ./..."), "it-key")
	if resp.status != http.StatusCreated {
		t.Fatalf("submit status = %d: %s", resp.status, resp.body)
	}
	var created jobBody
	resp.decode(t, &created)
	if created.State != "QUEUED" || created.Number < 1 || created.Workload.Spec.Runtime.Image != "golang:1.27" {
		t.Errorf("created = %+v", created)
	}
	if got, want := resp.header.Get("Location"), "/api/v1/jobs/"+created.ID.String(); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}

	var byID, byNumber jobBody
	if status := getJSON(t, srv.URL+"/api/v1/jobs/"+created.ID.String(), &byID); status != http.StatusOK {
		t.Fatalf("get by id status = %d", status)
	}
	if status := getJSON(t, fmt.Sprintf("%s/api/v1/jobs/%d", srv.URL, created.Number), &byNumber); status != http.StatusOK {
		t.Fatalf("get by number status = %d", status)
	}
	if byID.ID != created.ID || byNumber.ID != created.ID {
		t.Errorf("lookups returned %s and %s, want %s", byID.ID, byNumber.ID, created.ID)
	}
	if len(byID.Transitions) != 1 || byID.Transitions[0].From != nil || byID.Transitions[0].To != "QUEUED" ||
		byID.Transitions[0].Actor != "api" {
		t.Errorf("transitions = %+v", byID.Transitions)
	}
	if byID.Workload.ID != created.Workload.ID || byID.Workload.Spec.Command != "go test ./..." {
		t.Errorf("workload = %+v", byID.Workload)
	}

	var attempts struct {
		Attempts []json.RawMessage `json:"attempts"`
	}
	if status := getJSON(t, srv.URL+"/api/v1/jobs/"+created.ID.String()+"/attempts", &attempts); status != http.StatusOK ||
		attempts.Attempts == nil || len(attempts.Attempts) != 0 {
		t.Errorf("attempts status %d body %+v, want an empty list", status, attempts)
	}

	var logs struct {
		Chunks    []json.RawMessage `json:"chunks"`
		NextAfter int64             `json:"next_after"`
	}
	if status := getJSON(t, srv.URL+"/api/v1/jobs/"+created.ID.String()+"/logs", &logs); status != http.StatusOK ||
		len(logs.Chunks) != 0 || logs.NextAfter != 0 {
		t.Errorf("logs status %d body %+v, want empty", status, logs)
	}

	if status := getJSON(t, srv.URL+"/api/v1/jobs/"+uuid.Must(uuid.NewV7()).String(), nil); status != http.StatusNotFound {
		t.Errorf("unknown job status = %d, want 404", status)
	}
}

func TestSubmitIdempotencyOverHTTP(t *testing.T) {
	srv := newServer(t, testStale)

	first := submitWorkload(t, srv.URL, workloadJSON("make"), "same")
	if first.status != http.StatusCreated {
		t.Fatalf("first status = %d: %s", first.status, first.body)
	}
	var created jobBody
	first.decode(t, &created)

	replay := submitWorkload(t, srv.URL, workloadJSON("make"), "same")
	if replay.status != http.StatusOK || replay.header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status = %d, header = %q", replay.status, replay.header.Get("Idempotent-Replayed"))
	}
	var replayed jobBody
	replay.decode(t, &replayed)
	if replayed.ID != created.ID {
		t.Errorf("replay job = %s, want %s", replayed.ID, created.ID)
	}

	if resp := submitWorkload(t, srv.URL, workloadJSON("make lint"), "same"); resp.status != http.StatusConflict {
		t.Errorf("mismatched body status = %d, want 409", resp.status)
	}
	if resp := submitWorkload(t, srv.URL, `{"repository":"ftp://x","revision":"main","command":"x"}`, ""); resp.status != http.StatusUnprocessableEntity {
		t.Errorf("invalid workload status = %d, want 422", resp.status)
	}
}

func TestListJobsPagingAndEvents(t *testing.T) {
	srv := newServer(t, testStale)

	ids := make([]uuid.UUID, 5)
	for i := range ids {
		var b jobBody
		submitWorkload(t, srv.URL, workloadJSON(fmt.Sprintf("echo %d", i)), "").decode(t, &b)
		ids[i] = b.ID
	}

	type page struct {
		Jobs []struct {
			ID       uuid.UUID `json:"id"`
			Number   int64     `json:"number"`
			Workload struct {
				Command string `json:"command"`
			} `json:"workload"`
		} `json:"jobs"`
		NextBefore *int64 `json:"next_before"`
	}
	var seen []uuid.UUID
	url := srv.URL + "/api/v1/jobs?limit=2"
	for range 5 {
		var p page
		if status := getJSON(t, url, &p); status != http.StatusOK {
			t.Fatalf("list status = %d", status)
		}
		for _, j := range p.Jobs {
			seen = append(seen, j.ID)
		}
		if p.NextBefore == nil {
			break
		}
		url = fmt.Sprintf("%s/api/v1/jobs?limit=2&before=%d", srv.URL, *p.NextBefore)
	}
	if len(seen) != len(ids) {
		t.Fatalf("paged through %d jobs, want %d", len(seen), len(ids))
	}
	for i, id := range seen {
		if want := ids[len(ids)-1-i]; id != want {
			t.Errorf("page order[%d] = %s, want %s (newest first)", i, id, want)
		}
	}

	var queued, failed page
	getJSON(t, srv.URL+"/api/v1/jobs?state=QUEUED", &queued)
	getJSON(t, srv.URL+"/api/v1/jobs?state=FAILED", &failed)
	if len(queued.Jobs) != 5 || len(failed.Jobs) != 0 {
		t.Errorf("QUEUED = %d, FAILED = %d; want 5 and 0", len(queued.Jobs), len(failed.Jobs))
	}
	if queued.Jobs[0].Workload.Command != "echo 4" {
		t.Errorf("newest command = %q, want echo 4", queued.Jobs[0].Workload.Command)
	}

	var events struct {
		Events []struct {
			ID      int64           `json:"id"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		} `json:"events"`
		NextAfter int64 `json:"next_after"`
	}
	base := srv.URL + "/api/v1/jobs/" + ids[0].String() + "/events"
	if status := getJSON(t, base, &events); status != http.StatusOK {
		t.Fatalf("events status = %d", status)
	}
	if len(events.Events) != 1 || events.Events[0].Type != "job.created" || events.NextAfter != events.Events[0].ID {
		t.Fatalf("events = %+v", events)
	}
	var payload struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(events.Events[0].Payload, &payload); err != nil || payload.State != "QUEUED" {
		t.Errorf("payload = %s", events.Events[0].Payload)
	}
	cursor := events.NextAfter
	events.Events = nil
	getJSON(t, fmt.Sprintf("%s?after=%d", base, cursor), &events)
	if len(events.Events) != 0 || events.NextAfter != cursor {
		t.Errorf("after cursor: %+v, want no events and the same cursor", events)
	}
}
