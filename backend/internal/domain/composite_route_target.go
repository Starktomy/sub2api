package domain

// CompositeRouteTarget is one ordered provider target in a composite model
// route. Lower priorities are attempted first.
type CompositeRouteTarget struct {
	TargetPlatform string `json:"target_platform"`
	UpstreamModel  string `json:"upstream_model"`
	Priority       int    `json:"priority"`
	Enabled        bool   `json:"enabled"`
	Notes          string `json:"notes,omitempty"`
}
