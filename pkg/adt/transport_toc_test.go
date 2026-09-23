package adt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- Transport of copies ---

// fakeBridge stands in for ZADT_VSP's function bridge and records the calls.
type fakeBridge struct {
	mu    sync.Mutex
	calls []map[string]any
	subrc int
}

func (b *fakeBridge) CallRFC(_ context.Context, fn string, params map[string]any) (*RFCResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec := map[string]any{"_fn": fn}
	for k, v := range params {
		rec[k] = v
	}
	b.calls = append(b.calls, rec)
	return &RFCResult{Subrc: b.subrc}, nil
}

// transportXML renders the organizer's answer for one request. Objects in a
// task appear in the task and, aggregated, in all_objects -- as ADT sends it.
func transportXML(number, desc, status string, tasks map[string][]string) string {
	var all, tk strings.Builder
	for task, objs := range tasks {
		fmt.Fprintf(&tk, `<tm:task tm:number="%s" tm:parent="%s" tm:owner="TESTUSER" tm:status="%s">`, task, number, status)
		for _, o := range objs {
			obj := fmt.Sprintf(`<tm:abap_object tm:pgmid="R3TR" tm:type="PROG" tm:name="%s"/>`, o)
			tk.WriteString(obj)
			all.WriteString(obj)
		}
		tk.WriteString(`</tm:task>`)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><tm:root xmlns:tm="http://www.sap.com/cts/adt/tm">`+
		`<tm:request tm:number="%s" tm:owner="TESTUSER" tm:desc="%s" tm:type="K" tm:status="%s">`+
		`<tm:all_objects>%s</tm:all_objects>%s</tm:request></tm:root>`, number, desc, status, all.String(), tk.String())
}

type tocServer struct {
	mu       sync.Mutex
	created  []string // create bodies
	released []string // released numbers
}

func newTocClient(t *testing.T, source string, opts ...Option) (*Client, *tocServer) {
	t.Helper()
	ts := &tocServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CSRF-Token", "TOKEN")
		w.Header().Set("Content-Type", acceptTransportOrganizerV1)
		path := r.URL.Path
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/cts/transportrequests"):
			b, _ := io.ReadAll(r.Body)
			ts.mu.Lock()
			ts.created = append(ts.created, string(b))
			ts.mu.Unlock()
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><tm:root xmlns:tm="http://www.sap.com/cts/adt/tm">`+
				`<tm:request tm:number="TR-TOC" tm:type="T"/></tm:root>`)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/newreleasejobs"):
			ts.mu.Lock()
			ts.released = append(ts.released, strings.Split(strings.TrimPrefix(path, "/sap/bc/adt/cts/transportrequests/"), "/")[0])
			ts.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/transportrequests/TR-TOC"):
			_, _ = io.WriteString(w, transportXML("TR-TOC", "ToC", "D", nil))
		case r.Method == http.MethodGet && strings.Contains(path, "/transportrequests/"):
			_, _ = io.WriteString(w, source)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	opts = append([]Option{WithEnableTransports()}, opts...)
	cfg := NewConfig(srv.URL, "TESTUSER", "secret", opts...)
	return NewClientWithTransport(cfg, NewTransport(cfg)), ts
}

func copiedFrom(b *fakeBridge) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, c := range b.calls {
		if c["_fn"] == "TR_COPY_COMM" {
			out = append(out, fmt.Sprint(c["WI_TRKORR_FROM"]))
		}
	}
	return out
}

// A modifiable request keeps its objects in its tasks; TR_COPY_COMM on the
// request itself copies only the request's own entries (none), so each task
// with objects is copied, the way SE01's include-objects does it.
func TestCopyToTransportOfCopies_CopiesEachTaskOfAModifiableRequest(t *testing.T) {
	src := transportXML("TR-SRC", "[DEMO-1] Feature", "D", map[string][]string{
		"TR-TASK1": {"ZDEMO_A"},
		"TR-TASK2": {},
	})
	client, ts := newTocClient(t, src, WithCTSProject("PRJ_DEMO"))
	bridge := &fakeBridge{}

	res, err := client.copyToTransportOfCopies(context.Background(), bridge, "tr-src", TransportOfCopiesOptions{Target: "QAS"})
	if err != nil {
		t.Fatalf("copyToTransportOfCopies: %v", err)
	}
	if res.Transport != "TR-TOC" {
		t.Errorf("transport = %q, want TR-TOC", res.Transport)
	}
	if got := copiedFrom(bridge); len(got) != 1 || got[0] != "TR-TASK1" {
		t.Errorf("copied from %v, want only the task that holds objects [TR-TASK1]", got)
	}
	bridge.mu.Lock()
	call := bridge.calls[0]
	bridge.mu.Unlock()
	for k, want := range map[string]string{"WI_DIALOG": "", "WI_TRKORR_TO": "TR-TOC", "WI_WITHOUT_DOCUMENTATION": "X"} {
		if fmt.Sprint(call[k]) != want {
			t.Errorf("TR_COPY_COMM %s = %q, want %q", k, call[k], want)
		}
	}

	body := ts.created[0]
	for _, want := range []string{`tm:type="T"`, `tm:target="QAS"`, `tm:desc="ToC [DEMO-1] Feature"`, `tm:cts_project="PRJ_DEMO"`} {
		if !strings.Contains(body, want) {
			t.Errorf("create body lacks %s\nbody: %s", want, body)
		}
	}
	if strings.Contains(body, "tm:task") {
		t.Errorf("a transport of copies has no tasks, but the body asks for one: %s", body)
	}
	if len(ts.released) != 0 {
		t.Errorf("released %v without being asked", ts.released)
	}
}

// Release moves the tasks' entries into the request, so a released request
// is copied from the request alone -- copying its tasks too would double them.
func TestCopyToTransportOfCopies_CopiesAReleasedRequestFromTheRequest(t *testing.T) {
	src := transportXML("TR-SRC", "demo", "R", map[string][]string{"TR-TASK1": {"ZDEMO_A"}})
	client, _ := newTocClient(t, src)
	bridge := &fakeBridge{}

	if _, err := client.copyToTransportOfCopies(context.Background(), bridge, "TR-SRC", TransportOfCopiesOptions{Target: "QAS"}); err != nil {
		t.Fatalf("copyToTransportOfCopies: %v", err)
	}
	if got := copiedFrom(bridge); len(got) != 1 || got[0] != "TR-SRC" {
		t.Errorf("copied from %v, want [TR-SRC]", got)
	}
}

func TestCopyToTransportOfCopies_RefusesBeforeWriting(t *testing.T) {
	src := transportXML("TR-SRC", "demo", "D", map[string][]string{"TR-TASK1": {"ZDEMO_A"}})

	t.Run("no target", func(t *testing.T) {
		client, ts := newTocClient(t, src)
		if _, err := client.copyToTransportOfCopies(context.Background(), &fakeBridge{}, "TR-SRC", TransportOfCopiesOptions{}); err == nil {
			t.Fatal("accepted a transport of copies without a target")
		}
		if len(ts.created) != 0 {
			t.Error("created a request before refusing")
		}
	})
	t.Run("no bridge", func(t *testing.T) {
		client, ts := newTocClient(t, src)
		if _, err := client.CopyToTransportOfCopies(context.Background(), nil, "TR-SRC", TransportOfCopiesOptions{Target: "QAS"}); err == nil {
			t.Fatal("went ahead without the function bridge")
		}
		if len(ts.created) != 0 {
			t.Error("created a request it could not fill")
		}
	})
	t.Run("nothing to copy", func(t *testing.T) {
		empty := transportXML("TR-SRC", "demo", "D", map[string][]string{"TR-TASK1": {}})
		client, ts := newTocClient(t, empty)
		if _, err := client.copyToTransportOfCopies(context.Background(), &fakeBridge{}, "TR-SRC", TransportOfCopiesOptions{Target: "QAS"}); err == nil {
			t.Fatal("created an empty transport of copies")
		}
		if len(ts.created) != 0 {
			t.Error("created a request for nothing")
		}
	})
}

func TestCopyToTransportOfCopies_AFailedCopyNamesTheRequestItLeft(t *testing.T) {
	src := transportXML("TR-SRC", "demo", "D", map[string][]string{"TR-TASK1": {"ZDEMO_A"}})
	client, ts := newTocClient(t, src)

	res, err := client.copyToTransportOfCopies(context.Background(), &fakeBridge{subrc: 4}, "TR-SRC",
		TransportOfCopiesOptions{Target: "QAS", Release: true})
	if err == nil {
		t.Fatal("a failed TR_COPY_COMM was reported as success")
	}
	if res == nil || res.Transport != "TR-TOC" || !strings.Contains(err.Error(), "TR-TOC") {
		t.Errorf("the error must name the request that now exists: res=%+v err=%v", res, err)
	}
	if len(ts.released) != 0 {
		t.Error("released a transport of copies whose copy failed")
	}
}

func TestCopyToTransportOfCopies_ReleasesWhenAsked(t *testing.T) {
	src := transportXML("TR-SRC", "demo", "D", map[string][]string{"TR-TASK1": {"ZDEMO_A"}})
	client, ts := newTocClient(t, src)

	res, err := client.copyToTransportOfCopies(context.Background(), &fakeBridge{}, "TR-SRC",
		TransportOfCopiesOptions{Target: "QAS", Description: "own title", Release: true})
	if err != nil {
		t.Fatalf("copyToTransportOfCopies: %v", err)
	}
	if !res.Released || len(ts.released) != 1 || ts.released[0] != "TR-TOC" {
		t.Errorf("released=%v calls=%v, want TR-TOC released", res.Released, ts.released)
	}
	if !strings.Contains(ts.created[0], `tm:desc="own title"`) {
		t.Errorf("an explicit description was not used: %s", ts.created[0])
	}
}

func TestTransportOfCopiesDescription_FitsE07T(t *testing.T) {
	long := strings.Repeat("x", 80)
	if got := transportOfCopiesDescription(long); len([]rune(got)) != 60 || !strings.HasPrefix(got, "ToC ") {
		t.Errorf("description %q (%d runes), want 60 starting with \"ToC \"", got, len([]rune(got)))
	}
}
