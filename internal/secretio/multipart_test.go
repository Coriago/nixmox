package secretio

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"nixmox/internal/domain"
	"strings"
	"testing"
)

func TestSecretIngressRejectsExtraPartsAndBoundsMetadata(t *testing.T) {
	metadata := domain.SecretImport{ID: "cmd_import", Name: "token", Kind: "git-http-token", StorageNodeID: "n_holder"}
	for _, extra := range []bool{false, true} {
		var body bytes.Buffer
		parts := multipart.NewWriter(&body)
		part, _ := parts.CreateFormField("metadata")
		_ = json.NewEncoder(part).Encode(metadata)
		part, _ = parts.CreateFormField("value")
		_, _ = io.WriteString(part, "fixture-secret")
		if extra {
			part, _ = parts.CreateFormField("unexpected")
			_, _ = io.WriteString(part, "must not be ignored")
		}
		parts.Close()
		r := httptest.NewRequest("POST", "/api/v1/credentials/import", &body)
		r.Header.Set("Content-Type", parts.FormDataContentType())
		request, value, err := Read(httptest.NewRecorder(), r)
		if err != nil || request != metadata {
			t.Fatal(request, err)
		}
		data, err := io.ReadAll(value)
		value.Close()
		if extra && err == nil {
			t.Fatal("extra secret part accepted")
		}
		if !extra && (err != nil || string(data) != "fixture-secret") {
			t.Fatal("secret bytes changed", err)
		}
	}
	body, kind := Encode(metadata, strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	defer body.Close()
	r := httptest.NewRequest("POST", "/api/v1/credentials/import", body)
	r.Header.Set("Content-Type", kind)
	_, value, err := Read(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	if _, err = io.Copy(io.Discard, value); err == nil {
		t.Fatal("oversized secret streamed successfully")
	}
}
