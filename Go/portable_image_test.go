package sdk

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Verify portable image data URI
func TestPortableImageDataURI(t *testing.T) {
	for _, mime := range []string{"image/png", "image/svg+xml"} {
		data := []byte{0, 1, 254, 255}
		got, err := ImageDataURI(mime, data)
		if err != nil || got != "data:"+mime+";base64,AAH+/w==" {
			t.Fatalf("got %q: %v", got, err)
		}
		if data[3] != 255 {
			t.Fatal("input mutated")
		}
		maximum := make([]byte, MaxPortableImageBytes)
		got, err = ImageDataURI(mime, maximum)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.SplitN(got, ",", 2)[1])
		if err != nil || len(raw) != len(maximum) {
			t.Fatal("boundary roundtrip failed")
		}
		for _, invalid := range [][]byte{nil, make([]byte, MaxPortableImageBytes+1)} {
			if _, err := ImageDataURI(mime, invalid); err == nil {
				t.Fatal("invalid size accepted")
			}
		}
	}
	for _, mime := range []string{"image/jpeg", "IMAGE/PNG", "image/svg+xml;charset=utf-8", "file:///tmp/icon.png"} {
		if _, err := ImageDataURI(mime, []byte{1}); err == nil {
			t.Fatal("invalid media type accepted")
		}
	}
}
