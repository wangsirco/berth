package version

// Version is set with -ldflags "-X github.com/wangsirco/berth/internal/version.Version=0.1.0".
var Version = "dev"

// ReleasePubKey is the base64 ed25519 public key that release manifests must be signed
// with; set at build time. Empty disables online updates.
var ReleasePubKey = ""

// CoreHash fingerprints the sources of this build (release.CoreHash), set at build time;
// a release whose core hash matches only needs its web UI swapped.
var CoreHash = ""
