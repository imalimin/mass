package catalog

import "testing"

func TestExtractSkillDescription(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want string
	}{
		{
			name: "standard frontmatter",
			md:   "---\nname: pdf-reader\ndescription: Extract text and tables from PDF files.\n---\n# PDF Reader\n",
			want: "Extract text and tables from PDF files.",
		},
		{
			name: "bom and crlf with folded description",
			md:   "\xef\xbb\xbf---\r\nname: demo\r\ndescription: >\r\n  First line\r\n  second line.\r\n---\r\nBody",
			want: "First line second line.",
		},
		{
			name: "missing description field",
			md:   "---\nname: only-name\n---\n# Heading\n",
			want: "",
		},
		{
			name: "no frontmatter",
			md:   "# Just markdown\n\ndescription: not frontmatter",
			want: "",
		},
		{
			name: "unclosed frontmatter",
			md:   "---\ndescription: unclosed\n",
			want: "",
		},
		{
			name: "invalid yaml",
			md:   "---\ndescription: [broken\n---\nbody",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractSkillDescription(tc.md); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
