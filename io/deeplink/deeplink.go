package deeplink

import (
	"net/url"
)

// Event is generated when the app is opened from a deeplink, or when the app is already running and a deeplink is opened.
type Event struct {
	URL *url.URL
}

func (Event) ImplementsEvent() {}
