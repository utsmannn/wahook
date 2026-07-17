package payload

import "testing"

func TestIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		p    *Payload
		want bool
	}{
		{"unknown no text no media", &Payload{Type: "unknown"}, true},
		{"unknown empty text", &Payload{Type: "unknown", Text: ""}, true},
		{"text with body", &Payload{Type: "text", Text: "hi"}, false},
		{"unknown with text (edge)", &Payload{Type: "unknown", Text: "x"}, false},
		{"unknown with media (edge)", &Payload{Type: "unknown", Media: &MediaInfo{MimeType: "image/png"}}, false},
		{"reaction", &Payload{Type: "reaction", Text: "👍"}, false},
		{"audio", &Payload{Type: "audio", Media: &MediaInfo{MimeType: "audio/ogg"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.IsEmpty(); got != c.want {
				t.Errorf("IsEmpty() = %v, want %v", got, c.want)
			}
		})
	}
}
