package push

import (
	"net/http"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDefaultRegionExtractor(t *testing.T) {

	Convey("Given a request without an X-Region header", t, func() {

		r, err := http.NewRequest(http.MethodGet, "http://localhost/", nil)
		if err != nil {
			panic(err)
		}

		So(defaultRegionExtractor(r), ShouldEqual, defaultRegion)
	})

	Convey("Given a request with an X-Region header", t, func() {

		r, err := http.NewRequest(http.MethodGet, "http://localhost/", nil)
		if err != nil {
			panic(err)
		}

		r.Header.Set("X-Region", "eu")

		So(defaultRegionExtractor(r), ShouldEqual, "eu")
	})
}
