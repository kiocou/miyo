package app

// Where listeners reach the app's author and its installers, besides
// GitHub, which many of their networks cannot reach.

// updateMirrors are the prefixes the updater fetches a release on GitHub
// through when GitHub does not answer: public GitHub proxies, measured
// reachable from China. The updates are verified with the app's key
// whatever they come through, so a proxy only carries the bytes.
var updateMirrors = []string{
	"https://gh-proxy.com/",
	"https://ghfast.top/",
}

// manualDownloadURL can point to a fallback mirror/cloud drive share when
// GitHub releases cannot be reached directly.
var manualDownloadURL = ""

// feedbackBilibiliUID can point to a maintainer's Bilibili UID for direct feedback.
var feedbackBilibiliUID int64 = 0
