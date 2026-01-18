package events

// Subject naming: <prefix>.<domain>.<name>
// Prefix is configured per deployment (e.g. "mona").

const (
	DomainNetwork = "network"
	DomainPoll    = "poll"
	DomainDevice  = "device"
	DomainAlert   = "alert"
)

const (
	NetworkDeviceDiscovered = DomainNetwork + ".device_discovered"
	NetworkObserved         = DomainNetwork + ".observed"

	PollRequest = DomainPoll + ".request"
	PollResult  = DomainPoll + ".result"

	DeviceStateUpdated = DomainDevice + ".state_updated"

	AlertRaised = DomainAlert + ".raised"
)

