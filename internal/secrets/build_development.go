//go:build !gitvaultrelease

package secrets

// NativeDevelopmentBuild keeps development credentials in a separate service
// namespace. Distribution builds must use the explicit gitvaultrelease tag.
const NativeDevelopmentBuild = true
