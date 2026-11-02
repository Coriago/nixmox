package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

const LogRetention = 5 * 24 * time.Hour

var subjectID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type Logs struct{ JS jetstream.JetStream }
type LogEntry struct {
	Sequence uint64          `json:"sequence"`
	Time     time.Time       `json:"time"`
	Data     json.RawMessage `json:"data"`
}

func LogSubject(kind, id string) (string, error) {
	if (kind != "host" && kind != "task") || !subjectID.MatchString(id) {
		return "", errors.New("invalid log subject")
	}
	return "logs." + kind + "." + id, nil
}

// Provision is called by the cluster coordinator, not by individual log writers.
func (l Logs) Provision(ctx context.Context, replicas int) error {
	_, err := l.JS.CreateStream(ctx, jetstream.StreamConfig{Name: "LOGS", Subjects: []string{"logs.host.*", "logs.task.*"}, Storage: jetstream.FileStorage, Replicas: replicas, MaxAge: LogRetention})
	return err
}

func (l Logs) Append(ctx context.Context, kind, id string, payload *collectlogs.ExportLogsServiceRequest) (uint64, error) {
	subject, err := LogSubject(kind, id)
	if err != nil {
		return 0, err
	}
	if payload == nil {
		return 0, errors.New("log payload required")
	}
	data, err := protojson.Marshal(payload)
	if err != nil {
		return 0, err
	}
	msg := nats.NewMsg(subject)
	msg.Data = data
	Inject(ctx, msg)
	ack, err := l.JS.PublishMsg(ctx, msg)
	if err != nil {
		return 0, err
	}
	return ack.Sequence, nil
}

// Follow uses one ordered consumer for history and live delivery. The caller
// persists Sequence as its SSE event ID and supplies it on reconnect. History
// older than the retention window cannot be replayed.
func (l Logs) Follow(ctx context.Context, kind, id string, after uint64, handle func(LogEntry) error) error {
	subject, err := LogSubject(kind, id)
	if err != nil {
		return err
	}
	if after == ^uint64(0) {
		return errors.New("log cursor overflow")
	}
	stream, err := l.JS.Stream(ctx, "LOGS")
	if err != nil {
		return err
	}
	consumer, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: []string{subject}, DeliverPolicy: jetstream.DeliverByStartSequencePolicy, OptStartSeq: after + 1, InactiveThreshold: time.Minute})
	if err != nil {
		return err
	}
	messages, err := consumer.Messages(jetstream.PullMaxMessages(100))
	if err != nil {
		return err
	}
	defer messages.Stop()
	for {
		msg, err := messages.Next(jetstream.NextContext(ctx))
		if err != nil {
			return err
		}
		meta, err := msg.Metadata()
		if err != nil {
			return err
		}
		if meta.Sequence.Stream <= after {
			continue
		}
		if !json.Valid(msg.Data()) {
			return fmt.Errorf("invalid retained log at sequence %d", meta.Sequence.Stream)
		}
		if err = handle(LogEntry{Sequence: meta.Sequence.Stream, Time: meta.Timestamp, Data: append(json.RawMessage(nil), msg.Data()...)}); err != nil {
			return err
		}
		after = meta.Sequence.Stream
	}
}

// Read returns a bounded page of the history that existed when the call began.
// The final sequence is used as the next cursor, including for live follow.
func (l Logs) Read(ctx context.Context, kind, id string, after uint64, limit int) ([]LogEntry, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("log limit must be between 1 and 500")
	}
	subject, err := LogSubject(kind, id)
	if err != nil {
		return nil, err
	}
	stream, err := l.JS.Stream(ctx, "LOGS")
	if err != nil {
		return nil, err
	}
	last, err := stream.GetLastMsgForSubject(ctx, subject)
	if errors.Is(err, jetstream.ErrMsgNotFound) {
		return []LogEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []LogEntry{}
	if last.Sequence <= after {
		return result, nil
	}
	end := errors.New("log page complete")
	err = l.Follow(ctx, kind, id, after, func(entry LogEntry) error {
		if entry.Sequence > last.Sequence {
			return end
		}
		result = append(result, entry)
		if entry.Sequence == last.Sequence || len(result) == limit {
			return end
		}
		return nil
	})
	if errors.Is(err, end) {
		err = nil
	}
	return result, err
}
