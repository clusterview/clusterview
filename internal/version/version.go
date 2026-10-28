// Package version holds build information, set at build time with -ldflags.
package version

var (
	// Version is the release version, such as "v0.1.0".
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "unknown"
)
