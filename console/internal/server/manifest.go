package server

import (
	"bytes"
	"html/template"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// hiddenValue is the literal replacing every masked value (annotation values
// and the inline basic-auth credential), so the manifest page can show which
// fields exist without leaking their contents.
const hiddenValue = "(hidden)"

// pruneManifest reduces the raw cluster object to "what the user applied": the
// identifying fields plus spec, dropping status and all server-managed
// metadata. Annotation KEYS are kept but every value is replaced with
// hiddenValue, as is spec.auth.passwordHash (an inline htpasswd credential).
// Empty labels/annotations are omitted rather than rendered as an empty map.
// masked reports whether anything was hidden, driving the security note.
// The source object is never mutated (it may come from a shared cache).
func pruneManifest(obj *unstructured.Unstructured) (pruned map[string]any, masked bool) {
	out := map[string]any{}
	if v, ok := obj.Object["apiVersion"]; ok {
		out["apiVersion"] = v
	}
	if v, ok := obj.Object["kind"]; ok {
		out["kind"] = v
	}

	m := map[string]any{}
	if v := obj.GetName(); v != "" {
		m["name"] = v
	}
	if v := obj.GetNamespace(); v != "" {
		m["namespace"] = v
	}
	if labels := obj.GetLabels(); len(labels) > 0 {
		m["labels"] = labels
	}
	if ann := obj.GetAnnotations(); len(ann) > 0 {
		hidden := map[string]any{}
		for k := range ann {
			hidden[k] = hiddenValue
		}
		m["annotations"] = hidden
		masked = true
	}
	if len(m) > 0 {
		out["metadata"] = m
	}

	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		if auth, ok := spec["auth"].(map[string]any); ok {
			if _, ok := auth["passwordHash"]; ok {
				spec = runtime.DeepCopyJSON(spec)
				spec["auth"].(map[string]any)["passwordHash"] = hiddenValue
				masked = true
			}
		}
		out["spec"] = spec
	}
	return out, masked
}

// yamlLexer/yamlFormatter are package-level so the formatter's style cache
// amortizes the token-class CSS derivation across requests, and Coalesce
// merges adjacent same-type tokens into one span per run.
var (
	yamlLexer = func() chroma.Lexer {
		if l := lexers.Get("yaml"); l != nil {
			return chroma.Coalesce(l)
		}
		return nil
	}()
	yamlFormatter = html.New(html.WithClasses(true))
)

// highlightYAML syntax-highlights YAML into class-based HTML (colours come
// from CSS, not inline styles). On ANY chroma error it falls back to the plain
// escaped YAML wrapped in a <pre> so the page never breaks.
func highlightYAML(raw string) template.HTML {
	if yamlLexer == nil {
		return plainPre(raw)
	}
	iterator, err := yamlLexer.Tokenise(nil, raw)
	if err != nil {
		return plainPre(raw)
	}
	var buf bytes.Buffer
	if err := yamlFormatter.Format(&buf, styles.Fallback, iterator); err != nil {
		return plainPre(raw)
	}
	return template.HTML(buf.String())
}

func plainPre(raw string) template.HTML {
	return template.HTML("<pre>" + template.HTMLEscapeString(raw) + "</pre>")
}
