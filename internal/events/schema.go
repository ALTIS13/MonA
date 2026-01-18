package events

import (
	_ "embed"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/desc/protoparse"
	"github.com/jhump/protoreflect/dynamic"
)

//go:embed events.proto
var eventsProto string

const protoFileName = "events.proto"

type Schema struct {
	Envelope        *desc.MessageDescriptor
	DeviceDiscovered *desc.MessageDescriptor
	NetworkObserved *desc.MessageDescriptor
	PollRequest     *desc.MessageDescriptor
	PollResult      *desc.MessageDescriptor
	DeviceStateUpdated *desc.MessageDescriptor
	AlertRaised     *desc.MessageDescriptor
}

var (
	schemaOnce sync.Once
	schemaInst *Schema
	schemaErr  error
)

func LoadSchema() (*Schema, error) {
	schemaOnce.Do(func() {
		p := protoparse.Parser{
			Accessor: func(filename string) (io.ReadCloser, error) {
				if filename == protoFileName {
					return io.NopCloser(strings.NewReader(eventsProto)), nil
				}
				return nil, fmt.Errorf("unknown import: %s", filename)
			},
		}
		fds, err := p.ParseFiles(protoFileName)
		if err != nil {
			schemaErr = err
			return
		}
		fd := fds[0]
		schemaInst = &Schema{
			Envelope:           fd.FindMessage("mona.events.v1.Envelope"),
			DeviceDiscovered:   fd.FindMessage("mona.events.v1.DeviceDiscovered"),
			NetworkObserved:    fd.FindMessage("mona.events.v1.NetworkObserved"),
			PollRequest:        fd.FindMessage("mona.events.v1.PollRequest"),
			PollResult:         fd.FindMessage("mona.events.v1.PollResult"),
			DeviceStateUpdated: fd.FindMessage("mona.events.v1.DeviceStateUpdated"),
			AlertRaised:        fd.FindMessage("mona.events.v1.AlertRaised"),
		}
		if schemaInst.Envelope == nil {
			schemaErr = fmt.Errorf("schema: missing Envelope descriptor")
		}
	})
	return schemaInst, schemaErr
}

func (s *Schema) NewEnvelope(subject string) *dynamic.Message {
	m := dynamic.NewMessage(s.Envelope)
	m.SetFieldByName("id", NewID())
	m.SetFieldByName("ts_unix_ms", time.Now().UTC().UnixMilli())
	m.SetFieldByName("subject", subject)
	return m
}

