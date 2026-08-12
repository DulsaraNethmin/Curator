package indexer

import "testing"

func TestParseQuality(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// The reason the explicit-token loop runs before the 4K/UHD check. This
		// exact release is in the fixture: it is a 4.2 GB 1080p encode whose name
		// says UHD. Matching UHD first files it as 2160p and it turns up in a 4K
		// filter as a "4K" that is not one.
		{"Interstellar.2014.PROPER.IMAX.1080p.UHD.BluRay.x265.HDR.DV.DD+5.1.Dual.YG⭐", "1080p"},
		{"Interstellar 2014 UHD BluRay 2160p DTS-HD MA 5 1 HEVC REMUX-FraMeSToR", "2160p"},
		{"Some.Movie.2020.4K.1080p.WEB-DL", "1080p"},

		// 4K and UHD still mean 2160p when no explicit token is present.
		{"Some.Movie.2020.UHD.BluRay.REMUX.HDR", "2160p"},
		{"Some.Movie.2020.4K.WEB-DL.DDP5.1", "2160p"},
		{"Faster Than Light 'The Dream Of Interstellar Flight' (2017) 4K 2160p 29.97fps", "2160p"},

		{"Interstellar (2014) 1080p BrRip x264 - YIFY", "1080p"},
		{"Interstellar (2014) 720p BrRip x264 - YIFY", "720p"},
		{"Interstellar.2014.1440p.WEB-DL", "1440p"},
		{"Some.Old.Movie.1998.480p.DVDRip.XviD", "480p"},
		{"Some.Phone.Rip.360p.mp4", "360p"},

		// Case-insensitive: plenty of uploaders write 1080P or 4k.
		{"SOME.MOVIE.2019.1080P.BLURAY", "1080p"},
		{"some.movie.2019.4k.bluray", "2160p"},

		// Best-first: a pack naming several resolutions reports the best one.
		{"Some.Movie.2160p.and.1080p.dual", "2160p"},

		// No resolution at all — both of these are real fixture rows.
		{"VA-DJ Cube & DJ Kamelot - Interstellar Travel 10-2020 (MelissaPerry)", QualityUnknown},
		{"(18+) Lolita From Interstellar Space (2014) WEB-RIP [HDTV](450MB)", QualityUnknown},
		{"", QualityUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseQuality(tt.name); got != tt.want {
				t.Errorf("parseQuality(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestQualityRank(t *testing.T) {
	if QualityRank("2160p") >= QualityRank("1080p") {
		t.Error("2160p must rank ahead of 1080p")
	}
	if QualityRank("1080p") >= QualityRank("720p") {
		t.Error("1080p must rank ahead of 720p")
	}
	if QualityRank("720p") >= QualityRank(QualityUnknown) {
		t.Error("a known quality must rank ahead of unknown")
	}
	if QualityRank("nonsense") != QualityRank(QualityUnknown) {
		t.Error("an unrecognised quality must sort last, like unknown")
	}
	if QualityRank("1080P") != QualityRank("1080p") {
		t.Error("rank must not care about case")
	}
}

func TestFilterQuality(t *testing.T) {
	in := []Release{
		{Title: "a", Quality: "2160p"},
		{Title: "b", Quality: "1080p"},
		{Title: "c", Quality: "720p"},
		{Title: "d", Quality: QualityUnknown},
		{Title: "e", Quality: "1080p"},
	}

	tests := []struct {
		label  string
		want   []string
		titles []string
	}{
		{"empty keeps everything", nil, []string{"a", "b", "c", "d", "e"}},
		{"empty slice keeps everything", []string{}, []string{"a", "b", "c", "d", "e"}},
		{"exact", []string{"1080p"}, []string{"b", "e"}},
		{"bare number gets its p", []string{"720"}, []string{"c"}},
		{"4k means 2160p", []string{"4k"}, []string{"a"}},
		{"4K uppercase", []string{"4K"}, []string{"a"}},
		{"several, mixed forms", []string{"1080", "4k"}, []string{"a", "b", "e"}},
		{"whitespace is trimmed", []string{"  1080p  "}, []string{"b", "e"}},
		{"blank entries are ignored", []string{"", "720"}, []string{"c"}},
		{"no match keeps nothing", []string{"480p"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := FilterQuality(in, tt.want)
			if len(got) != len(tt.titles) {
				t.Fatalf("FilterQuality(%v) returned %d releases, want %d", tt.want, len(got), len(tt.titles))
			}
			for i, title := range tt.titles {
				if got[i].Title != title {
					t.Errorf("result %d = %q, want %q", i, got[i].Title, title)
				}
			}
		})
	}
}
