package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	ID   string          `json:"id"`
	Type string          `json:"event"`
	Data json.RawMessage `json:"data"`
}
type callbackError struct{ error }
type protocolError struct{ error }

// Watch reconnects reads only. The cursor advances after the callback succeeds,
// so consumers can durably apply an event before acknowledging its delivery.
func (c *Client) Watch(ctx context.Context, path, lastID string, handle func(Event) error) error {
	delay := 500 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := c.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "text/event-stream")
		if lastID != "" {
			request.Header.Set("Last-Event-ID", lastID)
		}
		response, err := c.http.Do(request)
		if err == nil {
			if response.StatusCode == http.StatusNoContent {
				response.Body.Close()
				return nil
			}
			if response.StatusCode != http.StatusOK {
				err = responseError(response)
				response.Body.Close()
				if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
					return err
				}
			} else {
				if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
					response.Body.Close()
					return errors.New("daemon did not return an SSE stream")
				}
				err = readEvents(response.Body, &lastID, &delay, handle)
				response.Body.Close()
				var callback callbackError
				if errors.As(err, &callback) {
					return callback.error
				}
				var protocol protocolError
				if errors.As(err, &protocol) {
					return protocol.error
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func readEvents(reader io.Reader, lastID *string, retry *time.Duration, handle func(Event) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	event := Event{ID: *lastID, Type: "message"}
	var data strings.Builder
	first := true
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		if line == "" {
			if data.Len() > 0 {
				event.Data = json.RawMessage(strings.TrimSuffix(data.String(), "\n"))
				if err := handle(event); err != nil {
					return callbackError{err}
				}
			}
			*lastID = event.ID
			event = Event{ID: *lastID, Type: "message"}
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			if !strings.ContainsRune(value, 0) {
				event.ID = value
			}
		case "event":
			event.Type = value
		case "data":
			if data.Len()+len(value) > 1<<20 {
				return protocolError{errors.New("SSE event exceeds size limit")}
			}
			data.WriteString(value)
			data.WriteByte('\n')
		case "retry":
			millis, err := strconv.ParseUint(value, 10, 32)
			if err == nil {
				*retry = max(250*time.Millisecond, min(30*time.Second, time.Duration(millis)*time.Millisecond))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return protocolError{err}
		}
		return err
	}
	return io.EOF
}
