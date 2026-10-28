package cluster

import "strings"

// Annotations people can put on a workload (or its pod template) to describe
// it themselves. They win over everything clusterview works out.
const (
	AnnotationTitle       = "clusterview.io/title"
	AnnotationDescription = "clusterview.io/description"
	AnnotationCategory    = "clusterview.io/category"
)

// describe fills in a service's title, description and category, from the
// first of: the owner's annotations, the operator that runs it, the image it
// runs. Without any, the title is the Kubernetes name.
func describe(s *Service, members []*Workload, allJobs bool) {
	s.Title = s.Name
	s.Category = CategoryApp
	if allJobs {
		s.Category = CategoryJob
	}

	if op := s.ManagedBy; op != nil {
		if e, ok := operatorCatalog[op.Group()+"/"+op.Kind]; ok {
			apply(s, e, "operator")
		}
	} else {
		for _, m := range members {
			if e, ok := lookupImage(m.Image); ok {
				apply(s, e, "image")
				break
			}
		}
	}

	for _, m := range members {
		a := m.annotations
		if a[AnnotationTitle] == "" && a[AnnotationDescription] == "" && a[AnnotationCategory] == "" {
			continue
		}
		if t := a[AnnotationTitle]; t != "" {
			s.Title = t
		}
		if d := a[AnnotationDescription]; d != "" {
			s.Description = d
			s.DescriptionSource = "annotation"
		}
		if c := a[AnnotationCategory]; c != "" {
			s.Category = Category(c)
		}
		break
	}
}

func apply(s *Service, e catalogEntry, source string) {
	if !e.keepName {
		s.Title = e.title
	}
	s.Description = e.description
	s.DescriptionSource = source
	s.Category = e.category
}

// lookupImage finds a well-known image by its repository path, ignoring the
// registry, tag and digest: "registry.k8s.io/ingress-nginx/controller:v1.11"
// matches "ingress-nginx/controller".
func lookupImage(image string) (catalogEntry, bool) {
	path := imagePath(image)
	if path == "" {
		return catalogEntry{}, false
	}
	best, bestLen := catalogEntry{}, 0
	for pattern, e := range imageCatalog {
		if (path == pattern || strings.HasSuffix(path, "/"+pattern)) && len(pattern) > bestLen {
			best, bestLen = e, len(pattern)
		}
	}
	return best, bestLen > 0
}

func imagePath(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	parts := strings.Split(image, "/")
	// A first segment with a dot or port, or "localhost", is a registry host.
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	if len(parts) > 1 && parts[0] == "library" {
		parts = parts[1:]
	}
	return strings.Join(parts, "/")
}

// Guesser describes services nothing else could, from the image they run.
// Its answers are marked as guesses ("ai").
type Guesser interface {
	// Guess returns what's known about an image path so far.
	Guess(imagePath string) (Guess, bool)
	// Want asks for images to be described. The guesser calls the cache's
	// Refresh when it has new answers.
	Want(hints []ImageHint)
}

// Guess is what a guesser says about an image.
type Guess struct {
	Description string
	Category    Category
}

// ImageHint is what a guesser is told about an image: its path without the
// registry. Nothing about the workloads that run it, because one answer
// describes every service running that image.
type ImageHint struct {
	Image string `json:"image"`
}

// applyGuesses describes the services nothing else described, and asks for
// guesses about the rest.
func applyGuesses(s *Snapshot, g Guesser) {
	byID := make(map[string]*Workload, len(s.Workloads))
	for i := range s.Workloads {
		byID[s.Workloads[i].ID] = &s.Workloads[i]
	}
	var want []ImageHint
	for i := range s.Services {
		svc := &s.Services[i]
		if svc.DescriptionSource != "" {
			continue
		}
		var hint ImageHint
		categorised := false
		for _, id := range svc.Workloads {
			w := byID[id]
			if w == nil {
				continue
			}
			categorised = categorised || w.annotations[AnnotationCategory] != ""
			if hint.Image == "" && w.Image != "" {
				hint = ImageHint{Image: imagePath(w.Image)}
			}
		}
		if hint.Image == "" {
			continue
		}
		guess, ok := g.Guess(hint.Image)
		if !ok {
			want = append(want, hint)
			continue
		}
		svc.Description = guess.Description
		svc.DescriptionSource = "ai"
		if !categorised && svc.Category == CategoryApp && guess.Category != "" {
			svc.Category = guess.Category
		}
	}
	if len(want) > 0 {
		g.Want(want)
	}
}
