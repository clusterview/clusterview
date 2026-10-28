package ai

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/clusterview/clusterview/internal/cluster"
)

type cannedModel struct {
	reply string
	asked []Request
}

func (m *cannedModel) Name() string { return "canned" }
func (m *cannedModel) JSON(_ context.Context, r Request) ([]byte, error) {
	m.asked = append(m.asked, r)
	return []byte(m.reply), nil
}

func TestDescriber(t *testing.T) {
	m := &cannedModel{reply: `{"items":[
		{"image":"acme/payments","description":"Probably the team's own payments API.","category":"app"},
		{"image":"minio/minio","description":"MinIO, S3-compatible object storage.","category":"storage"},
		{"image":"not/asked","description":"Made up.","category":"app"},
		{"image":"acme/odd","description":"Something.","category":"spaceship"},
		{"image":"busybox","generic":true,"description":"BusyBox.","category":"platform"}]}`}
	d := NewDescriber(m, func() {}, slog.New(slog.DiscardHandler))
	d.Want([]cluster.ImageHint{{Image: "acme/payments"}, {Image: "minio/minio"}, {Image: "acme/odd"}, {Image: "busybox"}})
	d.Want([]cluster.ImageHint{{Image: "acme/payments"}}) // already queued

	batch := d.next()
	if len(batch) != 4 {
		t.Fatalf("batch: %+v", batch)
	}
	if err := d.describe(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.asked[0].User, `{"image":"minio/minio"}`) {
		t.Errorf("sent: %s", m.asked[0].User)
	}
	if g, ok := d.Guess("minio/minio"); !ok || g.Category != "storage" {
		t.Errorf("minio: %+v %v", g, ok)
	}
	if g, ok := d.Guess("acme/odd"); !ok || g.Category != "" {
		t.Errorf("an unknown category is dropped, the words kept: %+v %v", g, ok)
	}
	if _, ok := d.Guess("busybox"); ok {
		t.Error("a generic base image gets no description")
	}
	d.Want([]cluster.ImageHint{{Image: "busybox"}})
	if len(d.next()) != 0 {
		t.Error("a generic image isn't asked about again")
	}
	if _, ok := d.Guess("not/asked"); ok {
		t.Error("answers about images nobody asked about are ignored")
	}
	if len(d.next()) != 0 {
		t.Error("answered images leave the queue")
	}
	d.Want([]cluster.ImageHint{{Image: "minio/minio"}})
	if len(d.next()) != 0 {
		t.Error("known images aren't asked about again")
	}
}
