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
	return []Entry{
		{Vendor: "generic", Username: "root", Password: "root", Note: "common default"},
		{Vendor: "generic", Username: "root", Password: "admin", Note: "common default"},
		{Vendor: "generic", Username: "admin", Password: "admin", Note: "common default"},

		// Some farms use:
		{Vendor: "antminer", Username: "root", Password: "root", Note: "stock-ish on many images"},
		{Vendor: "whatsminer", Username: "root", Password: "root", Note: "varies by firmware"},
	}
}

