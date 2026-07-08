package chunk

import (
	"fmt"
	"strings"
)

// ApplyHierarchical groups child chunks into parents.
// Each parent is formed by concatenating consecutive children until
// parentMaxTokens is reached. Each child gets its parent's text and
// a parent_id assigned.
// Returns the same children slice but with ParentText and ParentID populated.
func ApplyHierarchical(children []Chunk, parentMaxTokens int) []Chunk {
	if len(children) == 0 || parentMaxTokens <= 0 {
		return children
	}

	// Build parent groups.
	type parentGroup struct {
		id   string
		text string
	}
	var parents []parentGroup
	var currentLines []string
	currentTokens := 0

	flushParent := func() {
		if len(currentLines) == 0 {
			return
		}
		pText := strings.Join(currentLines, "\n")
		pID := fmt.Sprintf("parent-%s", computeMD5(pText)[:8])
		parents = append(parents, parentGroup{id: pID, text: pText})
		currentLines = nil
		currentTokens = 0
	}

	// Track which parent each child belongs to.
	// We record the parent index for each child in the same pass.
	childParentIdx := make([]int, len(children))
	parentIdx := 0

	for i, ch := range children {
		t := EstimateTokens(ch.Text)
		if currentTokens+t > parentMaxTokens && len(currentLines) > 0 {
			flushParent()
			parentIdx++
		}
		currentLines = append(currentLines, ch.Text)
		currentTokens += t
		childParentIdx[i] = parentIdx
	}
	flushParent() // flush the last group

	// Assign ParentText and ParentID to each child.
	result := make([]Chunk, len(children))
	for i, ch := range children {
		pi := childParentIdx[i]
		if pi < len(parents) {
			ch.ParentText = parents[pi].text
			ch.ParentID = parents[pi].id
		}
		result[i] = ch
	}
	return result
}
