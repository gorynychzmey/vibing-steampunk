package adt

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// contextServer plays the ICM's part in a lock chain: a stateful LOCK opens a
// context (or joins the live one) and hands out its sap-contextid; a stateless
// request that arrives in that context ends it; a write is accepted only in the
// live context; and a second request arriving in a context that is still busy
// with one is refused with 400. A stateless request with no context gets a
// context id of its own, as SAP sends one on every response.
type contextServer struct {
	mu   sync.Mutex
	live string // the context the lock handles are bound to
	busy bool   // the live context is serving a request
	next int
}

func (s *contextServer) handler(w http.ResponseWriter, r *http.Request) {
	presented := ""
	if c, err := r.Cookie("sap-contextid"); err == nil {
		presented = c.Value
	}
	stateless := r.Header.Get("X-sap-adt-sessiontype") == "stateless"

	s.mu.Lock()
	inContext := presented != "" && presented == s.live
	if inContext && !stateless {
		if s.busy {
			s.mu.Unlock()
			http.Error(w, "session in use", http.StatusBadRequest)
			return
		}
		s.busy = true
		s.mu.Unlock()
		time.Sleep(2 * time.Millisecond) // long enough for a concurrent request to collide
		s.mu.Lock()
		s.busy = false
	}
	defer s.mu.Unlock()
	w.Header().Set("x-csrf-token", "TOKEN")

	switch {
	case r.URL.Query().Get("_action") == "LOCK":
		if !inContext {
			s.next++
			s.live = fmt.Sprintf("CTX-%d", s.next)
		}
		http.SetCookie(w, &http.Cookie{Name: "sap-contextid", Value: s.live, Path: "/"})
		s.next++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><asx:abap xmlns:asx="http://www.sap.com/abapxml">
		  <asx:values><DATA><LOCK_HANDLE>HANDLE-%d</LOCK_HANDLE>
		  <MODIFICATION_SUPPORT>Modification</MODIFICATION_SUPPORT></DATA></asx:values></asx:abap>`, s.next)
	case r.Method == http.MethodPut:
		if s.live == "" || presented != s.live {
			w.WriteHeader(http.StatusLocked)
			_, _ = w.Write([]byte(`<exc:exception xmlns:exc="http://www.sap.com/abapxml/types/communicationframework">` +
				`<type id="ExceptionResourceInvalidLockHandle"/><message lang="EN">invalid lock handle</message></exc:exception>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	case stateless:
		if presented != "" && presented == s.live {
			s.live = "" // a stateless request ends the context it arrives in
		}
		s.next++
		http.SetCookie(w, &http.Cookie{Name: "sap-contextid", Value: fmt.Sprintf("CTX-%d", s.next), Path: "/"})
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// Another caller's stateless read between LOCK and the write must neither end
// the context the handle is bound to nor replace its id in the jar.
func TestLockContext_AStatelessRequestInsideTheLockWindowLeavesTheContextAlone(t *testing.T) {
	s := &contextServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "TESTUSER", "pw")
	ctx := context.Background()

	objectURL := "/sap/bc/adt/programs/programs/ZDEMO"
	lock, err := c.LockObject(ctx, objectURL, "MODIFY")
	if err != nil {
		t.Fatalf("LockObject: %v", err)
	}
	if _, err := c.transport.Request(ctx, "/sap/bc/adt/repository/informationsystem/search", nil); err != nil {
		t.Fatalf("stateless read: %v", err)
	}
	if err := c.UpdateSource(ctx, objectURL+"/source/main", "REPORT zdemo.", lock.LockHandle, ""); err != nil {
		t.Fatalf("the write after a stateless read inside the lock window: %v", err)
	}
}

// Outside a lock window a stateless request keeps the jar, so it still ends
// the context a finished chain left behind instead of letting it linger.
func TestLockContext_OutsideTheLockWindowAStatelessRequestStillEndsTheContext(t *testing.T) {
	s := &contextServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "TESTUSER", "pw")
	ctx := context.Background()

	objectURL := "/sap/bc/adt/programs/programs/ZDEMO"
	lock, err := c.LockObject(ctx, objectURL, "MODIFY")
	if err != nil {
		t.Fatalf("LockObject: %v", err)
	}
	if err := c.UnlockObject(ctx, objectURL, lock.LockHandle); err != nil {
		t.Fatalf("UnlockObject: %v", err)
	}
	if _, err := c.transport.Request(ctx, "/sap/bc/adt/repository/informationsystem/search", nil); err != nil {
		t.Fatalf("stateless read: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != "" {
		t.Errorf("context %s outlived the chain: the stateless request no longer reached it", s.live)
	}
}

// Two callers running lock chains through one client while a third reads: the
// chains share the context, and every write has to land.
func TestLockContext_ConcurrentChainsAndReadersAllLand(t *testing.T) {
	s := &contextServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "TESTUSER", "pw")
	ctx := context.Background()

	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = c.transport.Request(ctx, "/sap/bc/adt/repository/informationsystem/search", nil)
		}
	}()

	var chains sync.WaitGroup
	errs := make(chan error, 40)
	for _, name := range []string{"ZDEMO_A", "ZDEMO_B"} {
		chains.Add(1)
		go func(objectURL string) {
			defer chains.Done()
			for i := 0; i < 20; i++ {
				lock, err := c.LockObject(ctx, objectURL, "MODIFY")
				if err != nil {
					errs <- err
					continue
				}
				if err := c.UpdateSource(ctx, objectURL+"/source/main", "REPORT x.", lock.LockHandle, ""); err != nil {
					errs <- err
				}
				if err := c.UnlockObject(ctx, objectURL, lock.LockHandle); err != nil {
					errs <- err
				}
			}
		}("/sap/bc/adt/programs/programs/" + name)
	}
	chains.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
