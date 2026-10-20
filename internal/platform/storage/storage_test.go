package storage

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Upload URLs go to browsers, so they are signed for the public endpoint when one is set,
// while the API's own calls keep using the private one.
func TestPresignUsesThePublicEndpoint(t *testing.T) {
	ctx := context.Background()
	sum := make([]byte, 32)
	for _, c := range []struct {
		name, public, wantHost string
	}{
		{"public endpoint set", "https://media.example.com", "media.example.com"},
		{"no public endpoint", "", "seaweedfs:8333"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := NewS3(Config{Endpoint: "http://seaweedfs:8333", PublicEndpoint: c.public, Region: "us-east-1", AccessKey: "k", SecretKey: "s"})
			up, err := s.PresignPut(ctx, "sprout-public", "a/b.png", "image/png", 10, sum, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(up.URL)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != c.wantHost || !strings.HasPrefix(u.Path, "/sprout-public/") {
				t.Fatalf("presigned URL %s, want host %s", up.URL, c.wantHost)
			}
			if got := s.client.Options().BaseEndpoint; got == nil || *got != "http://seaweedfs:8333" {
				t.Fatalf("API client endpoint changed to %v", got)
			}
		})
	}
}
