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

// setupHint derives the static note shown near the manifest's setup phase. The
// console deliberately does NOT compute or render the operator's default install
// command (no operator-config coupling — same precedent as an unset schedule
// rendering "operator default" rather than guessing a cron). Three shapes:
//   - setup.skip:true          → the spec opts out; nothing runs.
//   - setup omitted/empty AND pipeline.nodeVersion set → the operator injects a
//     default install for the packageManager; the exact command is echoed in the
//     build logs, so we point there rather than guessing it.
//   - anything else (explicit setup, or omitted with no nodeVersion / BYO) → no
//     hint: the YAML already speaks for itself, or nothing runs to explain.
func setupHint(obj *unstructured.Unstructured) string {
	spec, _ := obj.Object["spec"].(map[string]any)
	pipeline, _ := spec["pipeline"].(map[string]any)
	if pipeline == nil {
		return ""
	}
	setup, _ := nestedMap(pipeline, "phases", "setup")
	if skip, _ := setup["skip"].(bool); skip {
		return "setup skipped by spec"
	}
	if len(setup) > 0 {
		return "" // explicit BYO setup — the YAML shows it
	}
	// Setup is absent/empty. A default install only runs when nodeVersion is set;
	// without it the app is BYO and no setup phase exists, so there is nothing to
	// say.
	if _, ok := pipeline["nodeVersion"]; !ok {
		return ""
	}
	pm, _ := pipeline["packageManager"].(string)
	return "setup omitted — the operator runs its default install for " + pm +
		"; the exact command is echoed in the build logs"
}

// nestedMap walks a chain of map keys, returning the map at the end (or an empty
// map if any hop is missing or not a map, so callers can len()-test uniformly).
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return map[string]any{}, false
		}
		cur = next
	}
	return cur, true
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
