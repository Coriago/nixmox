package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/domain"
)

// WatchTask reconnects with a fresh task snapshot or the last delivered log offset.
// The callback runs synchronously; returning an error stops the watch.
func (c *Client) WatchTask(ctx context.Context, id domain.ID, logs bool, offset int64, receive func(json.RawMessage) error) error {
	topic := "tasks"
	if logs {
		topic = "task.logs"
	}
	var previous []byte
	return c.watch(ctx, func() url.Values {
		return url.Values{"topics": {topic}, "task_id": {string(id)}, "offset": {strconv.FormatInt(offset, 10)}}
	}, func(data json.RawMessage) error {
		if logs {
			var chunk domain.LogChunk
			if err := json.Unmarshal(data, &chunk); err != nil {
				return err
			}
			if chunk.DataBase64 == "" && !chunk.Complete {
				return nil
			}
			if err := receive(data); err != nil {
				return err
			}
			offset = chunk.NextOffset
		} else if !bytes.Equal(previous, data) {
			if err := receive(data); err != nil {
				return err
			}
			previous = bytes.Clone(data)
		}
		return nil
	})
}

// watch owns connection lifetime and reconnects; callers own snapshot/offset state.
// Rebuild the query only after consumption so log reconnects use delivered offsets.
func (c *Client) watch(ctx context.Context, query func() url.Values, receive func(json.RawMessage) error) error {
	transport := *c.http
	transport.Timeout = 0
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/events?"+query().Encode(), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "text/event-stream")
		response, err := transport.Do(request)
		if err == nil {
			err = consumeWatch(response, receive)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var network net.Error
		if err == nil || (!errors.Is(err, io.EOF) && !errors.As(err, &network)) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Our SSE producer emits single-line JSON data, including an explicit end marker.
func consumeWatch(response *http.Response, receive func(json.RawMessage) error) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		failure := &APIError{StatusCode: response.StatusCode}
		if err := json.NewDecoder(io.LimitReader(response.Body, domain.QueryTotalLimit)).Decode(&failure.Response); err != nil {
			return err
		}
		if failure.Response.Error == nil {
			return errors.New("invalid watch error response")
		}
		return failure
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), domain.QueryTotalLimit)
	event := ""
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			event = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if event == "done" {
				return nil
			}
			if err := receive(json.RawMessage(strings.TrimSpace(value))); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.EOF
}
