package cluster

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/httpapi"
	"nixmox/internal/network"
	"nixmox/internal/service"
	"nixmox/internal/storage"
)

func TestBrowserCreateUsesDurableService(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := config.Default()
	c.DataDir = st.Root
	c.NATS.ClientPort = -1
	c.NATS.RoutePort = -1
	c.NATS.LeafPort = -1
	m, err := Open(st, c)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, err := service.Open(st, network.Inspector{Config: c}, m.Operations()...)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := httptest.NewServer(httpapi.New(s, m))
	defer server.Close()
	form := url.Values{"id": {"cmd_browser_genesis"}, "target_id": {s.Identity().NodeID}, "type": {"cluster.create"}, "name": {"browser-lab"}}
	req, _ := http.NewRequest("POST", server.URL+"/ui/commands", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal(response.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Identity().ClusterID == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	response, err = server.Client().Get(server.URL + "/ui/cluster")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"browser-lab", "No full members", "unavailable while this node is joined"} {
		if !strings.Contains(string(body), text) {
			t.Fatalf("missing %q: %s", text, body)
		}
	}
	if strings.Contains(string(body), `name="type" value="cluster.create"`) {
		t.Fatal("joined UI offers replacement cluster")
	}
}
