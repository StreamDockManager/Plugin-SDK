package sdk

import (
	"encoding/base64"
	"fmt"
)

// MaxPortableImageBytes leaves room for the base64 image and protocol envelope
const MaxPortableImageBytes = 512 * 1024

// ImageDataURI encodes caller-provided PNG or SVG bytes for portable setImage
// messages. It never reads files or URLs, changes paths, or grants permissions
// It does not decode or sanitize images: only pass trusted image bytes. The
// ordinary outgoing message limit still applies to the complete message
func ImageDataURI(mediaType string, data []byte) (string, error) {
	if mediaType != "image/png" && mediaType != "image/svg+xml" {
		return "", fmt.Errorf("portable image media type must be image/png or image/svg+xml")
	}
	if len(data) == 0 || len(data) > MaxPortableImageBytes {
		return "", fmt.Errorf("portable image must contain 1..524288 bytes")
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
