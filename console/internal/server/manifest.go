package server

import (
	"bytes"
	"html/template"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// hiddenValue is the literal replacing every annotation value, so the manifest
// page can show which annotations exist without leaking their (often sensitive)
// contents.
const hiddenValue = "(hidden)"

// pruneManifest reduces the raw cluster object to "what the user applied": the
// identifying fields plus spec, dropping status and all server-managed metadata.
// Annotation KEYS are kept but every value is replaced with hiddenValue. Empty
// labels/annotations are omitted rather than rendered as an empty map.
func pruneManifest(obj *unstructured.Unstructured) map[string]any {
	src := obj.Object
	out := map[string]any{}
	if v, ok := src["apiVersion"]; ok {
		out["apiVersion"] = v
	}
	if v, ok := src["kind"]; ok {
		out["kind"] = v
	}

	if meta, ok := src["metadata"].(map[string]any); ok {
		m := map[string]any{}
		if v, ok := meta["name"]; ok {
			m["name"] = v
		}
		if v, ok := meta["namespace"]; ok {
			m["namespace"] = v
		}
		if labels, ok := meta["labels"].(map[string]any); ok && len(labels) > 0 {
			m["labels"] = labels
		}
		if ann, ok := meta["annotations"].(map[string]any); ok && len(ann) > 0 {
			hidden := map[string]any{}
			for k := range ann {
				hidden[k] = hiddenValue
			}
			m["annotations"] = hidden
		}
		out["metadata"] = m
	}

	if v, ok := src["spec"]; ok {
		out["spec"] = v
	}
	return out
}

// manifestHasAnnotations reports whether the pruned manifest carries any
// annotations, driving the security note.
func manifestHasAnnotations(pruned map[string]any) bool {
	meta, ok := pruned["metadata"].(map[string]any)
	if !ok {
		return false
	}
	ann, ok := meta["annotations"].(map[string]any)
	return ok && len(ann) > 0
}

// highlightYAML syntax-highlights YAML into class-based HTML (colours come from
// CSS, not inline styles). On ANY chroma error it falls back to the plain
// escaped YAML wrapped in a <pre> so the page never breaks.
func highlightYAML(raw string) template.HTML {
	fallback := template.HTML("<pre>" + template.HTMLEscapeString(raw) + "</pre>")

	lexer := lexers.Get("yaml")
	if lexer == nil {
		return fallback
	}
	iterator, err := lexer.Tokenise(nil, raw)
	if err != nil {
		return fallback
	}
	formatter := html.New(html.WithClasses(true), html.PreventSurroundingPre(false))
	var buf bytes.Buffer
	if err := formatter.Format(&buf, styles.Fallback, iterator); err != nil {
		return fallback
	}
	return template.HTML(buf.String())
}

// marshalManifest renders the pruned object to YAML text (the exact bytes the
// Copy button offers), returning "" on marshal error.
func marshalManifest(pruned map[string]any) string {
	b, err := yaml.Marshal(pruned)
	if err != nil {
		return ""
	}
	return string(b)
}
