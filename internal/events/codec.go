package events

import (
	"fmt"

	"github.com/jhump/protoreflect/dynamic"
)

func Marshal(m *dynamic.Message) ([]byte, error) {
	return m.Marshal()
}

func UnmarshalEnvelope(schema *Schema, b []byte) (*dynamic.Message, error) {
	if schema == nil || schema.Envelope == nil {
		return nil, fmt.Errorf("schema not loaded")
	}
	m := dynamic.NewMessage(schema.Envelope)
	if err := m.Unmarshal(b); err != nil {
		return nil, err
	}
	return m, nil
}

