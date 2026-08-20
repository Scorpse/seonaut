package buildinfo

var (
	ForkVersion      = "dev"
	ForkRevision     = "unknown"
	UpstreamRevision = "880b312c28fab8b0bf7fe4f9449dc4746dbb82ff"
	SchemaVersion    = "76"
)

type Info struct {
	ForkVersion      string `json:"fork_version"`
	ForkRevision     string `json:"fork_revision"`
	UpstreamRevision string `json:"upstream_revision"`
	SchemaVersion    string `json:"schema_version"`
}

func Current() Info {
	return Info{
		ForkVersion:      ForkVersion,
		ForkRevision:     ForkRevision,
		UpstreamRevision: UpstreamRevision,
		SchemaVersion:    SchemaVersion,
	}
}
