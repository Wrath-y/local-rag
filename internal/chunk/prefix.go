package chunk

import "strings"

// ApplyBreadcrumb prepends heading context as [H1 > H2 > H3] to chunk text.
// If headingStack is empty or maxDepth is 0, the text is returned unchanged.
// Example: headingStack=["Guide", "Setup", "Install"], maxDepth=3
//
//	→ "[Guide > Setup > Install]\n" + chunkText
func ApplyBreadcrumb(chunkText string, headingStack []string, maxDepth int) string {
	if len(headingStack) == 0 || maxDepth == 0 {
		return chunkText
	}

	depth := len(headingStack)
	if depth > maxDepth {
		depth = maxDepth
	}
	crumb := "[" + strings.Join(headingStack[:depth], " > ") + "]\n"
	return crumb + chunkText
}
