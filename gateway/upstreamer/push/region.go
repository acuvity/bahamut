package push

import "net/http"

// defaultRegion is the region announced by notifiers that do not
// set one explicitly, and the region assumed for pings received
// from notifiers that do not send a region at all.
const defaultRegion = "main"

// A RegionExtractor extracts the desired region from an incoming request.
type RegionExtractor func(r *http.Request) string

// defaultRegionExtractor returns the region carried in the X-Region
// header, or the default region if the header is not set.
func defaultRegionExtractor(r *http.Request) string {

	if region := r.Header.Get("X-Region"); region != "" {
		return region
	}

	return defaultRegion
}
