package broker

import (
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

type Stores struct {
	JS                nats.JetStreamContext
	State             nats.KeyValue
	Git               nats.ObjectStore
	Commands, Results string
}

func (b *Broker) Stores(replicas int) (*Stores, error) {
	if !b.config.ControlPlane || (replicas != 1 && replicas != 3) {
		return nil, errors.New("durable stores require control-plane membership")
	}
	js, err := b.Client.JetStream(nats.MaxWait(3 * time.Second))
	if err != nil {
		return nil, err
	}
	state, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "nx_state", Replicas: replicas, MaxBytes: 64 << 20, MaxValueSize: domain.MaxCommandBytes, Storage: nats.FileStorage, History: 1})
	if err != nil {
		return nil, err
	}
	git, err := js.CreateObjectStore(&nats.ObjectStoreConfig{Bucket: "nx_git", Replicas: replicas, MaxBytes: 1 << 30, Storage: nats.FileStorage})
	if err != nil {
		return nil, err
	}
	commands := "nx_commands"
	results := "nx_results"
	for _, v := range []struct {
		name, subject string
		count         int64
		age           time.Duration
	}{{commands, "nx." + b.config.ClusterID + ".command.*", 4096, 120 * time.Second}, {results, "nx." + b.config.ClusterID + ".result.*", 16384, 0}} {
		_, err := js.AddStream(&nats.StreamConfig{Name: v.name, Subjects: []string{v.subject}, Storage: nats.FileStorage, Replicas: replicas, Retention: nats.WorkQueuePolicy, Discard: nats.DiscardNew, MaxMsgs: v.count, MaxBytes: 64 << 20, MaxMsgSize: domain.MaxCommandBytes, MaxAge: v.age, MaxConsumers: 256, Duplicates: 2 * time.Minute})
		if err != nil {
			return nil, err
		}
	}
	return &Stores{JS: js, State: state, Git: git, Commands: commands, Results: results}, nil
}
