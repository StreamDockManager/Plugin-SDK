package authoring

import "github.com/StreamDockManager/Plugin-SDK/Go/inspectors"

// Select the embedded wide or square display layout
func DisplayLayout(wide bool) []byte {
	name := "key"
	if wide {
		name = "wide"
	}
	return inspectors.Assets()["display/"+name+".json"]
}
