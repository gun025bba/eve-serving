package webfetch

import (
	"strings"

	"golang.org/x/net/html"
)

var skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "iframe": true, "svg": true, "title": true, "object": true, "embed": true, "form": true, "nav": true, "footer": true}
var blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "tr": true, "section": true, "article": true, "pre": true, "blockquote": true}

// ExtractText returns the <title> and visible text of an HTML document,
// dropping script/style and hidden elements (hidden attr, aria-hidden,
// display:none / visibility:hidden inline styles) where prompt-injection
// payloads commonly hide.
func ExtractText(doc string) (title, text string) {
	n, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", doc
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode {
			if x.Data == "title" && title == "" && x.FirstChild != nil {
				title = strings.TrimSpace(x.FirstChild.Data)
			}
			if skipTags[x.Data] || hidden(x) {
				return
			}
			if blockTags[x.Data] {
				b.WriteString("\n")
			}
		}
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if x.Type == html.ElementNode && blockTags[x.Data] {
			b.WriteString("\n")
		}
	}
	walk(n)
	return title, b.String()
}

func hidden(x *html.Node) bool {
	for _, a := range x.Attr {
		k, v := strings.ToLower(a.Key), strings.ToLower(strings.ReplaceAll(a.Val, " ", ""))
		switch {
		case k == "hidden":
			return true
		case k == "aria-hidden" && v == "true":
			return true
		case k == "style" && (strings.Contains(v, "display:none") || strings.Contains(v, "visibility:hidden") || strings.Contains(v, "font-size:0")):
			return true
		case k == "type" && v == "hidden":
			return true
		}
	}
	return false
}
