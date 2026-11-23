package otlp

import "testing"

func TestDecodeRejectsNonOTLPMediaTypes(t *testing.T) {
	// An empty protobuf body unmarshals, so a non-JSON type that is accepted
	// comes back as success rather than a parse error.
	for _, contentType := range []string{
		"",
		"text/plain",
		"text/plain; charset=utf-8",
		"multipart/form-data",
		"application/x-www-form-urlencoded",
		"application/octet-stream",
		"application/protobuf",
	} {
		t.Run(contentType, func(t *testing.T) {
			if _, err := Decode("logs", nil, contentType); err == nil {
				t.Fatalf("Decode(logs, empty, %q) accepted a non-OTLP media type", contentType)
			}
		})
	}
}

func TestDecodeAcceptsJSONAndProtobufMediaTypes(t *testing.T) {
	for _, contentType := range []string{
		"application/json",
		"application/json; charset=utf-8",
		"Application/JSON",
		"application/x-protobuf",
		"application/x-protobuf; charset=binary",
		"Application/X-Protobuf",
	} {
		t.Run(contentType, func(t *testing.T) {
			body := []byte(nil)
			if mediaType(contentType) == "application/json" {
				body = []byte("{}")
			}
			if _, err := Decode("logs", body, contentType); err != nil {
				t.Fatalf("Decode(logs, %q): %v", contentType, err)
			}
		})
	}
}
