// Package secretio implements bounded secret ingress without logging bodies.
package secretio

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"nixmox/internal/domain"
)

func invalid() error {
	return domain.Err("INVALID_PAYLOAD", "Secret import requires bounded metadata followed by one value stream.", 422)
}

type finalPart struct {
	part  *multipart.Part
	parts *multipart.Reader
}

func (p *finalPart) Read(b []byte) (int, error) {
	n, err := p.part.Read(b)
	if err == io.EOF {
		_, next := p.parts.NextPart()
		if next != io.EOF {
			return n, invalid()
		}
	}
	return n, err
}
func (p *finalPart) Close() error { return p.part.Close() }
func Read(w http.ResponseWriter, r *http.Request) (domain.SecretImport, io.ReadCloser, error) {
	var request domain.SecretImport
	r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+(16<<10))
	parts, err := r.MultipartReader()
	if err != nil {
		return request, nil, invalid()
	}
	metadata, err := parts.NextPart()
	if err != nil || metadata.FormName() != "metadata" {
		return request, nil, invalid()
	}
	data, err := io.ReadAll(io.LimitReader(metadata, 4097))
	metadata.Close()
	if err != nil || len(data) > 4096 || domain.StrictJSON(data, &request) != nil {
		return request, nil, invalid()
	}
	value, err := parts.NextPart()
	if err != nil || value.FormName() != "value" {
		return request, nil, invalid()
	}
	return request, &finalPart{value, parts}, nil
}
func Encode(request domain.SecretImport, value io.Reader) (io.ReadCloser, string) {
	reader, writer := io.Pipe()
	parts := multipart.NewWriter(writer)
	go func() {
		var err error
		defer func() {
			if err != nil {
				writer.CloseWithError(errors.New("secret stream unavailable"))
			} else {
				writer.Close()
			}
		}()
		metadata, createErr := parts.CreateFormField("metadata")
		if createErr != nil {
			err = createErr
			return
		}
		if err = json.NewEncoder(metadata).Encode(request); err != nil {
			return
		}
		part, createErr := parts.CreateFormField("value")
		if createErr != nil {
			err = createErr
			return
		}
		var count int64
		count, err = io.Copy(part, io.LimitReader(value, (1<<20)+1))
		if err != nil {
			return
		}
		if count > 1<<20 {
			err = invalid()
			return
		}
		err = parts.Close()
	}()
	return reader, parts.FormDataContentType()
}
