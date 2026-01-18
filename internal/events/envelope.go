package events

import (
	"github.com/google/uuid"
)

func NewID() string { return uuid.NewString() }

func Subject(prefix, topic string) string {
	if prefix == "" {
		return topic
	}
	return prefix + "." + topic
}

