package defaultcreds

type Entry struct {
	Vendor   string `json:"vendor"`   // antminer/whatsminer/avalon/iceriver/elphapex/generic
	Username string `json:"username"`
	Password string `json:"password"`
	Note     string `json:"note"`
}

// Defaults are built-in and NOT stored in settings.json.
// This is meant for bootstrap discovery only. Custom creds come later (encrypted).
func Defaults() []Entry {
	// Intentionally empty by default to avoid trying random common pairs on production fleets.
	// Use encrypted Stored credentials in UI instead.
	return nil
}

