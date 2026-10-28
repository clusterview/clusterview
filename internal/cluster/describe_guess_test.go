package cluster

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
)

type fakeGuesser struct {
	known  map[string]Guess
	wanted []ImageHint
}

func (f *fakeGuesser) Guess(image string) (Guess, bool) { g, ok := f.known[image]; return g, ok }
func (f *fakeGuesser) Want(h []ImageHint)               { f.wanted = append(f.wanted, h...) }

func TestGuessesDescribeUnknownServices(t *testing.T) {
	objs := Objects{Deployments: append(dep("shop", "api"), dep("shop", "web")...)}
	objs.Deployments[1].Spec.Template.Spec.Containers[0].Image = "ghcr.io/acme/web:2.0"
	objs.Deployments[1].Annotations = map[string]string{AnnotationCategory: "ingress"}

	g := &fakeGuesser{known: map[string]Guess{"acme/web": {Description: "Probably the team's own website.", Category: CategoryStorage}}}
	s := BuildSnapshot(objs, 1, time.Now())
	applyGuesses(&s, g)

	if len(g.wanted) != 1 || g.wanted[0] != (ImageHint{Image: "acme/api"}) {
		t.Errorf("should ask about acme/api only, registry removed: %+v", g.wanted)
	}
	var web Service
	for _, svc := range s.Services {
		if svc.Name == "web" {
			web = svc
		}
	}
	if web.Description != "Probably the team's own website." || web.DescriptionSource != "ai" {
		t.Errorf("guess not applied: %+v", web)
	}
	if web.Category != "ingress" {
		t.Errorf("a category annotation beats a guess: got %s", web.Category)
	}
}

func dep(ns, name string) []*appsv1.Deployment {
	return []*appsv1.Deployment{deployment(ns, name, 1, 1)}
}
