package install

import "errors"

// Sentinel errors. Callers test them with errors.Is; Code maps them to the job error codes of ADMIN-API §3.
var (
	// ErrNotFound means the repository, the release or the version does not exist.
	ErrNotFound = errors.New("install: release not found")
	// ErrUpstream means GitHub or the asset host failed or answered nonsense.
	ErrUpstream = errors.New("install: upstream failure")
	// ErrRateLimited is an ErrUpstream caused by the GitHub API rate limit (set a token to raise it).
	ErrRateLimited = errors.New("install: GitHub API rate limit reached")
	// ErrInsecureURL means a URL (or a redirect) was not https.
	ErrInsecureURL = errors.New("install: only https URLs are allowed")
	// ErrNoAsset means the release has no build for this OS and CPU.
	ErrNoAsset = errors.New("install: no release asset for this OS and CPU")
	// ErrIncompatible means the plugin needs a newer hub (minHubVersion).
	ErrIncompatible = errors.New("install: plugin needs a newer hub version")
	// ErrBadManifest means homie-plugin.json is missing fields or has invalid ones.
	ErrBadManifest = errors.New("install: invalid homie-plugin.json")
	// ErrNoChecksum means checksums.txt is missing or has no line for the archive.
	ErrNoChecksum = errors.New("install: no checksum for the archive")
	// ErrChecksumMismatch means the downloaded bytes do not match checksums.txt.
	ErrChecksumMismatch = errors.New("install: checksum mismatch")
	// ErrTooLarge means a download or an archive exceeds its size limit.
	ErrTooLarge = errors.New("install: size limit exceeded")
	// ErrUnsafeArchive means the archive tried path traversal, a link escape or holds a forbidden entry type.
	ErrUnsafeArchive = errors.New("install: unsafe archive")
	// ErrStartFailed means the new version did not come up (the Start hook failed).
	ErrStartFailed = errors.New("install: the new version did not start")
	// ErrRolledBack marks an ErrStartFailed whose previous version was put back.
	ErrRolledBack = errors.New("install: rolled back to the previous version")
	// ErrUpToDate means the requested version is already installed.
	ErrUpToDate = errors.New("install: already up to date")
	// ErrDowngrade means the release is older than the installed version and the caller did not allow a downgrade.
	ErrDowngrade = errors.New("install: the release is older than the installed version")
	// ErrIDMismatch means the requested plugin id differs from the manifest's.
	ErrIDMismatch = errors.New("install: plugin id does not match the manifest")
	// ErrNotWritable means the running binary cannot be replaced (for example a package manager owns it).
	ErrNotWritable = errors.New("install: the binary location is not writable")
)

// Code maps an error to the job error codes of ADMIN-API (checksum_mismatch, no_asset, incompatible, start_failed,
// rolled_back, downgrade, upstream, internal) plus not_found and up_to_date. Unknown errors are "internal". Callers
// should put err.Error() in the job's message: it never contains a token.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDowngrade):
		return "downgrade"
	case errors.Is(err, ErrRolledBack):
		return "rolled_back"
	case errors.Is(err, ErrStartFailed):
		return "start_failed"
	case errors.Is(err, ErrChecksumMismatch), errors.Is(err, ErrNoChecksum):
		return "checksum_mismatch"
	case errors.Is(err, ErrNoAsset):
		return "no_asset"
	case errors.Is(err, ErrIncompatible):
		return "incompatible"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrUpToDate):
		return "up_to_date"
	case errors.Is(err, ErrUpstream), errors.Is(err, ErrBadManifest), errors.Is(err, ErrUnsafeArchive),
		errors.Is(err, ErrTooLarge), errors.Is(err, ErrInsecureURL):
		return "upstream"
	default:
		return "internal"
	}
}
