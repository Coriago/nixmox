package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nixmox/internal/cli"
)

func ExampleNewCommand() {
	// --- Test setup ---
	// ------------------
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"nodeId":"cedar","status":"online"}`)
	}))
	defer server.Close()
	var output bytes.Buffer
	command := cli.NewCommand(strings.NewReader(""), &output, io.Discard)

	// --- Usage ---
	// -------------
	command.SetArgs([]string{"--endpoint", server.URL, "--json", "health"})
	if err := command.ExecuteContext(context.Background()); err != nil {
		panic(err)
	}
	fmt.Print(output.String())

	// --- Assertions ---
	// ------------------

	// Output:
	// {"nodeId":"cedar","status":"online"}
}

func TestCredentialInputAndReview(t *testing.T) {
	// --- Test setup ---
	// ------------------
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Error("expected POST")
		}
		if r.URL.Path == "/credentials" {
			var input struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				Value []byte `json:"value"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if string(input.Value) != "example-token\n" || input.Name != "repo" || input.Type != "git" {
				t.Error("secret bytes or metadata changed")
			}
			clear(input.Value)
		} else {
			var input struct {
				OutputID string `json:"outputId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.OutputID != "review1" {
				t.Error("review output was not bound to approval")
			}
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	var output bytes.Buffer

	// --- Usage ---
	// -------------
	for _, args := range [][]string{{"credential", "put", "--name", "repo", "--type", "git", "--stdin"}, {"task", "approve", "build1", "--output", "review1"}} {
		command := cli.NewCommand(strings.NewReader("example-token\n"), &output, &output)
		command.SetArgs(append([]string{"--endpoint", server.URL}, args...))
		if err := command.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// --- Assertions ---
	// ------------------
	if len(paths) != 2 || paths[0] != "/credentials" || paths[1] != "/tasks/build1/approve" {
		t.Fatalf("unexpected requests: %v", paths)
	}
	if strings.Contains(output.String(), "example-token") {
		t.Fatal("secret leaked to command output")
	}
}
