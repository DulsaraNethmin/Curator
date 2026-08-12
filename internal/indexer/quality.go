package indexer

import "strings"

// QualityUnknown is what parseQuality returns for a name that carries no
// resolution at all.
const QualityUnknown = "—"

// qualityRank orders qualities best-first. Unknown sorts last.
var qualityRank = map[string]int{
	"2160p": 0, "1440p": 1, "1080p": 2, "720p": 3, "480p": 4, "360p": 5, QualityUnknown: 6,
}

// QualityRank orders qualities best-first, for ranking releases. Anything it does
// not recognise sorts last, alongside QualityUnknown.
func QualityRank(quality string) int {
	if r, ok := qualityRank[strings.ToLower(strings.TrimSpace(quality))]; ok {
		return r
	}
	return qualityRank[QualityUnknown]
}

// parseQuality reads the resolution out of a scene release name.
//
// Explicit resolution tokens are checked before the marketing ones, because names
// like "Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay" contain UHD while actually
// being 1080p. Matching UHD first would misreport those as 2160p — they then show
// up in a 4K filter as a 4 GB "4K" that is not one. Do not reorder this loop.
func parseQuality(name string) string {
	n := strings.ToUpper(name)
	for _, q := range []string{"2160P", "1440P", "1080P", "720P", "480P", "360P"} {
		if strings.Contains(n, q) {
			return strings.ToLower(q)
		}
	}
	if strings.Contains(n, "4K") || strings.Contains(n, "UHD") {
		return "2160p"
	}
	return QualityUnknown
}

// FilterQuality keeps only releases matching one of `want`. Empty `want` keeps all.
//
// Exported (cfprobe called it filterQuality) because filtering is a caller's
// decision, not the indexer's.
func FilterQuality(in []Release, want []string) []Release {
	if len(want) == 0 {
		return in
	}
	keep := make(map[string]bool, len(want))
	for _, q := range want {
		q = strings.ToLower(strings.TrimSpace(q))
		if q == "" {
			continue
		}
		// Accept "1080" as well as "1080p" — nobody wants to type the p.
		if !strings.HasSuffix(q, "p") && q != "4k" {
			q += "p"
		}
		if q == "4k" {
			q = "2160p"
		}
		keep[q] = true
	}
	var out []Release
	for _, r := range in {
		if keep[r.Quality] {
			out = append(out, r)
		}
	}
	return out
}
